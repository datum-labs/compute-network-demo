package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/datum"
	"github.com/datum-labs/compute-network-demo/internal/geo"
)

func fixtureInstances(t *testing.T) []datum.Instance {
	t.Helper()
	data, err := os.ReadFile("../datum/testdata/instances.json")
	if err != nil {
		t.Fatal(err)
	}
	insts, err := datum.ParseInstanceList(data)
	if err != nil {
		t.Fatal(err)
	}
	return insts
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

func TestFindSelf(t *testing.T) {
	insts := fixtureInstances(t)
	cases := []struct {
		name     string
		local    []netip.Addr
		hostname string
		want     string
	}{
		{"exact address", addrs("127.0.0.1", "::1", "fd20:0:2::3:0:0"), "", "xcheck-dfw-us-central-1-0"},
		{"address inside the instance prefix", addrs("::1", "fd20:0:2:1:0:2:0:9"), "", "xcheck-iad-us-east-1-0"},
		{"hostname fallback", addrs("10.1.2.3"), "gp-http-proof-default-us-central-1-0", "gp-http-proof-default-us-central-1-0"},
		{"no match", addrs("10.1.2.3", "fe80::1"), "laptop", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FindSelf(insts, c.local, c.hostname); got != c.want {
				t.Fatalf("FindSelf = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAssemble(t *testing.T) {
	insts := fixtureInstances(t)
	dfw, dfw2, iad := insts[0].Name, insts[1].Name, insts[2].Name
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	reports := map[string]*LocalReport{
		dfw: {Name: dfw, StartedAt: now.Add(-90 * time.Second), Peers: []PeerStats{
			{Name: dfw2, RTTMs: 0.8, SuccessRate: 1, Attempts: 10, Messages: 20, Bytes: 4000},
			{Name: iad, RTTMs: 31, SuccessRate: 0.5, Attempts: 10, Messages: 10, Bytes: 2000},
			{Name: "deleted-instance", RTTMs: 5, SuccessRate: 1, Attempts: 3, Messages: 6},
		}},
		dfw2: {Name: dfw2, StartedAt: now.Add(-time.Hour), Peers: []PeerStats{
			{Name: dfw, RTTMs: 1.2, SuccessRate: 1, Attempts: 10, Messages: 20, Bytes: 4000},
			{Name: iad, Attempts: 10, SuccessRate: 0},
		}},
		// iad did not report: its address was unreachable.
	}

	v := Assemble(insts, geo.NewDirectory(), dfw, reports, now)

	if v.Totals.Instances != 3 || v.Totals.Regions != 2 {
		t.Fatalf("totals = %+v", v.Totals)
	}
	if len(v.Edges) != 4 {
		t.Fatalf("edges = %+v, want 4 (unknown peers dropped)", v.Edges)
	}
	states := map[string]string{}
	for _, e := range v.Edges {
		states[e.From+">"+e.To] = e.State
	}
	want := map[string]string{
		dfw + ">" + dfw2: EdgeUp,
		dfw + ">" + iad:  EdgeDegraded,
		dfw2 + ">" + dfw: EdgeUp,
		dfw2 + ">" + iad: EdgeDown,
	}
	for k, w := range want {
		if states[k] != w {
			t.Errorf("edge %s state = %q, want %q", k, states[k], w)
		}
	}
	if v.Totals.Messages != 50 || v.Totals.Bytes != 10000 {
		t.Errorf("messages/bytes = %d/%d", v.Totals.Messages, v.Totals.Bytes)
	}
	if avg := v.Totals.AvgRTTMs; avg < 10.99 || avg > 11.01 {
		t.Errorf("avg rtt = %v, want 11 (down edges excluded)", avg)
	}
	if v.Totals.PublicInternetBytes != 0 {
		t.Error("public internet bytes must be zero")
	}

	byName := map[string]InstanceView{}
	for _, iv := range v.Instances {
		byName[iv.Name] = iv
	}
	self := byName[dfw]
	if !self.IsSelf || self.City != "Dallas" || self.Lat == 0 || self.PrivateIP != "fd20:0:f::1:0:0" {
		t.Errorf("self view = %+v", self)
	}
	if self.UptimeSeconds != 90 || self.PeersReachable != 2 || self.PeersTotal != 2 {
		t.Errorf("self uptime/peers = %d %d/%d", self.UptimeSeconds, self.PeersReachable, self.PeersTotal)
	}
	east := byName[iad]
	if east.City != "Ashburn" || east.Status != "starting" || east.Reporting {
		t.Errorf("iad view = %+v", east)
	}
	// Seen only from dfw, degraded but reachable.
	if east.PeersReachable != 1 {
		t.Errorf("iad reachable = %d, want 1", east.PeersReachable)
	}
}

func TestAssembleSingleInstance(t *testing.T) {
	insts := fixtureInstances(t)[:1]
	v := Assemble(insts, geo.NewDirectory(), insts[0].Name, map[string]*LocalReport{}, time.Now())
	if len(v.Instances) != 1 || len(v.Edges) != 0 || v.Totals.AvgRTTMs != 0 || v.Instances[0].PeersTotal != 0 {
		t.Fatalf("view = %+v", v)
	}
}

func TestEdgeState(t *testing.T) {
	cases := map[string]PeerStats{
		EdgePending:  {},
		EdgeUp:       {Attempts: 5, SuccessRate: 0.95},
		EdgeDegraded: {Attempts: 5, SuccessRate: 0.4},
		EdgeDown:     {Attempts: 5},
	}
	for want, s := range cases {
		if got := EdgeState(s); got != want {
			t.Errorf("EdgeState(%+v) = %q, want %q", s, got, want)
		}
	}
}

func TestPingerMeasuresPeers(t *testing.T) {
	srv := httptest.NewServer(NewHandler(staticSource{}, fstest.MapFS{}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	p := NewPinger(port, time.Hour, time.Second)
	p.SetPeers([]Peer{
		{Name: "up", Addr: netip.MustParseAddr(host)},
		// Nothing listens on this port on the documentation prefix.
		{Name: "down", Addr: netip.MustParseAddr("192.0.2.1")},
	})
	p.Timeout = 200 * time.Millisecond
	p.client.Timeout = 200 * time.Millisecond
	for range 3 {
		p.round(context.Background())
	}

	stats := p.Stats()
	if len(stats) != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	down, up := stats[0], stats[1]
	if up.Attempts != 3 || up.SuccessRate != 1 || up.Messages != 6 || up.RTTMs <= 0 || up.Bytes <= 0 {
		t.Errorf("up = %+v", up)
	}
	if down.Attempts != 3 || down.SuccessRate != 0 || down.Messages != 0 {
		t.Errorf("down = %+v", down)
	}

	// Replacing the peer set keeps history for peers that remain.
	p.SetPeers([]Peer{{Name: "up", Addr: netip.MustParseAddr(host)}})
	if s := p.Stats(); len(s) != 1 || s[0].Attempts != 3 {
		t.Errorf("after SetPeers = %+v", s)
	}
}

type staticSource struct{}

func (staticSource) View(context.Context) View { return View{Self: "a"} }
func (staticSource) Local() LocalReport {
	return LocalReport{Name: "a", Location: "us-central-1", StartedAt: time.Now()}
}

type fakeLister struct {
	instances []datum.Instance
	err       error
}

func (f *fakeLister) ListInstances(context.Context, string) ([]datum.Instance, error) {
	return f.instances, f.err
}
func (f *fakeLister) ListLocations(context.Context) ([]geo.Place, error) {
	return nil, errors.New("not needed")
}

func TestLiveViewCollectsPeerReports(t *testing.T) {
	insts := fixtureInstances(t)
	l := &Live{
		API:               &fakeLister{instances: insts},
		Workload:          "global-mesh",
		Port:              8080,
		Pinger:            NewPinger(8080, time.Hour, time.Second),
		Directory:         geo.NewDirectory(),
		CacheFor:          time.Minute,
		PeerReportTimeout: time.Second,
		LocalAddrs:        func() []netip.Addr { return addrs("fd20:0:f::1:0:0") },
		Hostname:          "x",
	}
	l.startedAt = time.Now()
	var calls atomic.Int32
	l.fetchPeer = func(_ context.Context, inst datum.Instance) (*LocalReport, error) {
		calls.Add(1)
		if inst.Location == "us-east-1" {
			return nil, errors.New("timeout")
		}
		return &LocalReport{Name: inst.Name, StartedAt: time.Now(), Peers: []PeerStats{
			{Name: insts[0].Name, RTTMs: 1, SuccessRate: 1, Attempts: 2, Messages: 4},
		}}, nil
	}

	l.discover(context.Background())
	if l.self != insts[0].Name {
		t.Fatalf("self = %q", l.self)
	}
	if n := len(l.Pinger.Stats()); n != 2 {
		t.Fatalf("pinger has %d peers, want 2", n)
	}

	v := l.View(context.Background())
	if v.Mode != "live" || v.Self != insts[0].Name || len(v.Instances) != 3 || v.Notice != "" {
		t.Fatalf("view = %+v", v)
	}
	// Self has not pinged yet, so its edges are pending; the unreachable
	// east instance contributes none.
	if len(v.Edges) != 3 || v.Edges[2].From != insts[1].Name || v.Edges[0].State != EdgePending {
		t.Fatalf("edges = %+v", v.Edges)
	}
	// Cached within CacheFor.
	l.View(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("peer fetches = %d, want 2 (cached second view)", calls.Load())
	}
}

func TestLiveNoticeBeforeDiscovery(t *testing.T) {
	l := &Live{API: &fakeLister{err: errors.New("401")}, Pinger: NewPinger(8080, time.Hour, time.Second),
		Directory: geo.NewDirectory(), LocalAddrs: func() []netip.Addr { return nil }}
	l.discover(context.Background())
	v := l.View(context.Background())
	if v.Notice == "" || len(v.Instances) != 0 {
		t.Fatalf("view = %+v", v)
	}
}

func TestSimulator(t *testing.T) {
	s := NewSimulator("global-mesh", geo.NewDirectory(), false, false)
	s.View(context.Background())
	time.Sleep(20 * time.Millisecond)
	v := s.View(context.Background())
	// Every location sits at its floor because scaling is off: two replicas
	// each across the three launch locations.
	if v.Totals.Instances != 6 || v.Totals.Regions != 3 || len(v.Edges) != 30 {
		t.Fatalf("totals = %+v edges = %d", v.Totals, len(v.Edges))
	}
	for _, e := range v.Edges {
		if e.State != EdgeUp || e.RTTMs <= 0 {
			t.Fatalf("edge = %+v", e)
		}
	}
	if !v.Instances[0].IsSelf && v.Self == "" {
		t.Fatal("no self in simulated fleet")
	}
	// Dallas to Ashburn should look like a real cross-country round trip.
	dfw, _ := geo.Static("us-central-1")
	iad, _ := geo.Static("us-east-1")
	if rtt := SimulatedRTT(dfw, iad); rtt < 20 || rtt > 45 {
		t.Errorf("DFW-IAD simulated rtt = %.1f", rtt)
	}
	// Replicas sharing a location are sub-millisecond apart, which is what
	// makes the intra-region links worth drawing.
	if rtt := SimulatedRTT(dfw, dfw); rtt <= 0 || rtt >= 1 {
		t.Errorf("same-location simulated rtt = %.1f", rtt)
	}

	if len(v.Activity) != 0 {
		t.Errorf("a fleet that never scales reported %d activity events", len(v.Activity))
	}

	faulty := NewSimulator("global-mesh", geo.NewDirectory(), false, true).View(context.Background())
	down := 0
	for _, e := range faulty.Edges {
		if e.State == EdgeDown {
			down++
		}
	}
	// One replica pair, seen from both ends.
	if down != 2 {
		t.Errorf("faults produced %d down edges, want 2", down)
	}
}

func TestHandler(t *testing.T) {
	site := fstest.MapFS{
		"index.html":    {Data: []byte("<html>mesh</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	srv := httptest.NewServer(NewHandler(staticSource{}, site))
	defer srv.Close()

	get := func(path string) (*http.Response, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return resp, buf[:n]
	}

	resp, body := get("/mesh/ping")
	var ping map[string]any
	if err := json.Unmarshal(body, &ping); err != nil || ping["name"] != "a" || resp.StatusCode != 200 {
		t.Fatalf("ping = %s (%v)", body, err)
	}
	if len(body) > 512 {
		t.Errorf("ping payload is %d bytes, keep it small", len(body))
	}
	if _, body := get("/api/mesh"); !json.Valid(body) {
		t.Errorf("api/mesh = %s", body)
	}
	if _, body := get("/some/deep/link"); string(body) != "<html>mesh</html>" {
		t.Errorf("spa fallback = %s", body)
	}
	if resp, _ := get("/assets/app.js"); resp.Header.Get("Cache-Control") == "" {
		t.Error("assets not cacheable")
	}
}

func TestParseStaticPeers(t *testing.T) {
	peers, err := ParseStaticPeers(" us-central-1=fd20:0:12::2:0:0, us-central-1=fd20:0:12::3:0:0/96,us-east-1=fd20:0:12:1:0:1::,")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, location, ip string }{
		{"us-central-1-0", "us-central-1", "fd20:0:12::2:0:0"},
		{"us-central-1-1", "us-central-1", "fd20:0:12::3:0:0"},
		{"us-east-1-0", "us-east-1", "fd20:0:12:1:0:1::"},
	}
	if len(peers) != len(want) {
		t.Fatalf("peers = %+v", peers)
	}
	for i, w := range want {
		if peers[i].Name != w.name || peers[i].Location != w.location || peers[i].PrivateIP.String() != w.ip || !peers[i].Available {
			t.Errorf("peer %d = %+v, want %+v", i, peers[i], w)
		}
	}
	for _, bad := range []string{"fd20::1", "us-east-1=nope", "=fd20::1"} {
		if _, err := ParseStaticPeers(bad); err == nil {
			t.Errorf("ParseStaticPeers(%q) succeeded", bad)
		}
	}
	if peers, err := ParseStaticPeers(""); err != nil || len(peers) != 0 {
		t.Errorf("empty spec = %v, %v", peers, err)
	}
}

func TestLiveFallsBackToStaticPeers(t *testing.T) {
	static, _ := ParseStaticPeers("us-central-1=fd20:0:12::2:0:0,us-east-1=fd20:0:12:1:0:1::")
	lister := &fakeLister{err: errors.New("dial tcp: no route to host")}
	l := &Live{
		API:         lister,
		StaticPeers: static,
		Pinger:      NewPinger(8080, time.Hour, time.Second),
		Directory:   geo.NewDirectory(),
		LocalAddrs:  func() []netip.Addr { return addrs("fd20:0:12:1:0:1::") },
	}
	l.fetchPeer = func(context.Context, datum.Instance) (*LocalReport, error) { return nil, errors.New("down") }

	l.discover(context.Background())
	v := l.View(context.Background())
	if v.Discovery != DiscoveryStatic || v.Notice != "" || v.Self != "us-east-1-0" || len(v.Instances) != 2 {
		t.Fatalf("view = %+v", v)
	}
	// Both launch locations resolve to real cities.
	for _, iv := range v.Instances {
		if iv.City == "" || iv.City == iv.Location || iv.Lat == 0 {
			t.Errorf("instance %s did not resolve: %+v", iv.Name, iv)
		}
	}

	// When the API becomes reachable it takes over again.
	lister.err = nil
	lister.instances = fixtureInstances(t)
	l.discover(context.Background())
	l.cachedAt = time.Time{}
	if v := l.View(context.Background()); v.Discovery != DiscoveryAPI || len(v.Instances) != 3 {
		t.Fatalf("after recovery view = %+v", v)
	}

	// Without an API client at all, static peers are used directly.
	l2 := &Live{StaticPeers: static, Pinger: NewPinger(8080, time.Hour, time.Second), Directory: geo.NewDirectory(),
		LocalAddrs: func() []netip.Addr { return nil }}
	l2.fetchPeer = l.fetchPeer
	l2.discover(context.Background())
	if v := l2.View(context.Background()); v.Discovery != DiscoveryStatic || len(v.Instances) != 2 {
		t.Fatalf("static-only view = %+v", v)
	}
}

func TestPingerRecordsExchanges(t *testing.T) {
	srv := httptest.NewServer(NewHandler(staticSource{}, fstest.MapFS{}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	p := NewPinger(port, time.Hour, 200*time.Millisecond)
	p.client.Timeout = 200 * time.Millisecond
	p.SetPeers([]Peer{
		{Name: "up", Addr: netip.MustParseAddr(host)},
		{Name: "down", Addr: netip.MustParseAddr("192.0.2.1")},
	})
	for range 2 {
		p.round(context.Background())
	}

	all := p.Exchanges(0)
	if len(all) != 4 {
		t.Fatalf("got %d exchanges, want 4", len(all))
	}
	// Newest first.
	for i := 1; i < len(all); i++ {
		if all[i].At.After(all[i-1].At) {
			t.Fatalf("exchanges out of order: %+v", all)
		}
	}
	var ok, failed int
	for _, e := range all {
		if e.OK {
			ok++
			if e.To != "up" || e.RTTMs <= 0 {
				t.Errorf("successful exchange = %+v", e)
			}
		} else {
			failed++
			if e.To != "down" {
				t.Errorf("failed exchange = %+v", e)
			}
		}
	}
	if ok != 2 || failed != 2 {
		t.Errorf("ok/failed = %d/%d, want 2/2", ok, failed)
	}
	if n := len(p.Exchanges(3)); n != 3 {
		t.Errorf("limit ignored: got %d", n)
	}

	// The buffer keeps only recent history.
	for range exchangeWindow {
		p.round(context.Background())
	}
	if n := len(p.Exchanges(0)); n != exchangeWindow {
		t.Errorf("buffer grew to %d, want %d", n, exchangeWindow)
	}
}

func TestAssembleMergesExchanges(t *testing.T) {
	insts := fixtureInstances(t)
	a, b := insts[0].Name, insts[1].Name
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	reports := map[string]*LocalReport{
		a: {Name: a, Exchanges: []Exchange{
			{At: now.Add(-1 * time.Second), To: b, RTTMs: 1.2, OK: true},
			{At: now.Add(-9 * time.Second), To: "gone", RTTMs: 4, OK: true},
		}},
		b: {Name: b, Exchanges: []Exchange{
			{At: now.Add(-3 * time.Second), To: a, RTTMs: 1.1, OK: true},
			{At: now.Add(-2 * time.Second), To: insts[2].Name, OK: false},
		}},
	}

	v := Assemble(insts, geo.NewDirectory(), a, reports, now)
	if len(v.Exchanges) != 3 {
		t.Fatalf("exchanges = %+v, want 3 (peer that no longer exists dropped)", v.Exchanges)
	}
	// Newest first, each attributed to its sender.
	if v.Exchanges[0].From != a || v.Exchanges[0].To != b {
		t.Errorf("newest = %+v", v.Exchanges[0])
	}
	if v.Exchanges[1].From != b || v.Exchanges[1].OK {
		t.Errorf("second = %+v", v.Exchanges[1])
	}
	for i := 1; i < len(v.Exchanges); i++ {
		if v.Exchanges[i].At.After(v.Exchanges[i-1].At) {
			t.Fatalf("not newest first: %+v", v.Exchanges)
		}
	}
}

func TestSimulatorProducesFeed(t *testing.T) {
	s := NewSimulator("global-mesh", geo.NewDirectory(), false, false)
	s.View(context.Background())
	time.Sleep(1200 * time.Millisecond)
	v := s.View(context.Background())

	if len(v.Exchanges) == 0 {
		t.Fatal("no exchanges in the simulated feed")
	}
	if len(v.Exchanges) > MaxFeedExchanges {
		t.Fatalf("feed carries %d exchanges, want at most %d", len(v.Exchanges), MaxFeedExchanges)
	}
	senders := map[string]bool{}
	spread := map[int64]bool{}
	for _, e := range v.Exchanges {
		if e.From == "" || e.To == "" || e.From == e.To {
			t.Fatalf("malformed exchange %+v", e)
		}
		if e.OK && e.RTTMs <= 0 {
			t.Errorf("successful exchange without a round trip: %+v", e)
		}
		senders[e.From] = true
		spread[e.At.UnixMilli()/100] = true
	}
	if len(senders) < 2 {
		t.Errorf("feed only shows %d sender(s); it should cover the fleet", len(senders))
	}
	// Staggered rather than all landing at once.
	if len(spread) < 3 {
		t.Errorf("exchanges bunched into %d slots; they should be spread out", len(spread))
	}

	faulty := NewSimulator("global-mesh", geo.NewDirectory(), false, true)
	faulty.View(context.Background())
	time.Sleep(1200 * time.Millisecond)
	failed := 0
	for _, e := range faulty.View(context.Background()).Exchanges {
		if !e.OK {
			failed++
		}
	}
	if failed == 0 {
		t.Error("DEMO_FAULTS produced no failed exchanges in the feed")
	}
}

func TestSimulatorScalesAndNarrates(t *testing.T) {
	s := NewSimulator("global-mesh", geo.NewDirectory(), true, false)
	base := s.start
	at := base
	s.now = func() time.Time { return at }

	seen := map[string]map[int]bool{}
	statuses := map[string]bool{}
	kinds := map[string]int{}
	changedAt := map[string][]int{}
	var joins []float64
	// Three minutes of a fleet nobody touched.
	for step := 0; step <= 180; step++ {
		at = base.Add(time.Duration(step) * time.Second)
		v := s.View(context.Background())
		per := map[string]int{}
		for _, inst := range v.Instances {
			statuses[inst.Status] = true
			if inst.Status != StatusStopping {
				per[inst.Location]++
			}
		}
		for location, n := range per {
			if seen[location] == nil {
				seen[location] = map[int]bool{}
			}
			if !seen[location][n] && len(seen[location]) > 0 {
				changedAt[location] = append(changedAt[location], step)
			}
			seen[location][n] = true
		}
		if len(v.Activity) > MaxActivity {
			t.Fatalf("activity carries %d events, want at most %d", len(v.Activity), MaxActivity)
		}
		for i := 1; i < len(v.Activity); i++ {
			if v.Activity[i].At.After(v.Activity[i-1].At) {
				t.Fatalf("activity is not newest first: %+v", v.Activity)
			}
		}
		for _, e := range v.Activity {
			kinds[e.Type]++
			if e.Location == "" {
				t.Fatalf("activity event without a location: %+v", e)
			}
			if e.Type == ActivityReady {
				joins = append(joins, e.JoinMs)
			}
		}
	}

	// Dallas breathes widely; every location comes back to its floor.
	if !seen["us-central-1"][5] || !seen["us-central-1"][2] {
		t.Errorf("us-central-1 counts = %v, want to reach both 2 and 5", seen["us-central-1"])
	}
	if !seen["us-west-1"][4] || !seen["us-east-1"][3] {
		t.Errorf("us-west-1 = %v, us-east-1 = %v; both should scale", seen["us-west-1"], seen["us-east-1"])
	}
	// Not in lockstep: the regions make their decisions at different moments.
	if sameSteps(changedAt["us-central-1"], changedAt["us-west-1"]) {
		t.Errorf("us-central-1 and us-west-1 scaled together: %v", changedAt)
	}
	for _, want := range []string{StatusStarting, StatusRunning, StatusStopping} {
		if !statuses[want] {
			t.Errorf("no instance was ever %q", want)
		}
	}
	for _, want := range []string{ActivityScaledUp, ActivityScaledDown, ActivityStarting, ActivityReady, ActivityStopping} {
		if kinds[want] == 0 {
			t.Errorf("no %q event in three minutes of activity", want)
		}
	}
	if len(joins) == 0 {
		t.Fatal("no instance reported how long it took to join the mesh")
	}
	for _, ms := range joins {
		// Derived from the lifecycle, not a constant: the join is the step
		// after the boot, and lasts well under two seconds.
		if ms < 600 || ms > 1700 {
			t.Errorf("join time = %.0f ms, want a plausible mesh join", ms)
		}
	}
}

// TestSimulatorIsDeterministic pins the property the whole simulator rests on:
// the fleet is a function of elapsed time, so two simulators started together
// agree at every moment however they are polled.
func TestSimulatorIsDeterministic(t *testing.T) {
	a := NewSimulator("global-mesh", geo.NewDirectory(), true, false)
	b := NewSimulator("global-mesh", geo.NewDirectory(), true, false)
	b.start = a.start
	for step := 0; step <= 200; step += 3 {
		at := a.start.Add(time.Duration(step) * time.Second)
		want, got := a.fleetAt(at), b.fleetAt(at)
		if len(want) != len(got) {
			t.Fatalf("at %ds: %d instances vs %d", step, len(want), len(got))
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("at %ds: %+v vs %+v", step, want[i], got[i])
			}
		}
	}
}

func sameSteps(a, b []int) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
