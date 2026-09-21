package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/datum"
	"github.com/datum-labs/compute-network-demo/internal/geo"
)

// Source produces the fleet view and this instance's own report.
type Source interface {
	View(ctx context.Context) View
	Local() LocalReport
}

// InstanceLister is the discovery dependency of Live.
type InstanceLister interface {
	ListInstances(ctx context.Context, workload string) ([]datum.Instance, error)
	ListLocations(ctx context.Context) ([]geo.Place, error)
}

// Discovery sources reported in the view.
const (
	DiscoveryAPI       = "datum-api"
	DiscoveryStatic    = "static"
	DiscoverySimulated = "simulated"
)

// Live discovers the workload's instances through the Datum Cloud API,
// measures traffic to each of them, and collects their measurements to show
// the whole mesh.
type Live struct {
	// API is the primary discovery source; it may be nil when no credentials
	// are available.
	API InstanceLister
	// StaticPeers is used whenever the API is absent or unreachable, for
	// networks without a route to the Datum Cloud API.
	StaticPeers       []datum.Instance
	Project, Workload string
	Port              int
	Pinger            *Pinger
	Directory         *geo.Directory
	DiscoverEvery     time.Duration
	LocationsEvery    time.Duration
	CacheFor          time.Duration
	PeerReportTimeout time.Duration
	// LocalAddrs and Hostname identify this instance; they default to the
	// machine's interfaces and hostname.
	LocalAddrs func() []netip.Addr
	Hostname   string
	// SelfName, when set, names this instance outright instead of working it
	// out from addresses. Useful when several instances share a host.
	SelfName string

	startedAt time.Time
	client    *http.Client

	mu           sync.RWMutex
	instances    []datum.Instance
	self         string
	selfLocation string
	discoverErr  error
	discovered   bool
	discovery    string

	viewMu    sync.Mutex
	cached    View
	cachedAt  time.Time
	activity  *activityLog
	fetchPeer func(ctx context.Context, inst datum.Instance) (*LocalReport, error)
}

// Start begins discovery and traffic. It returns immediately.
func (l *Live) Start(ctx context.Context) {
	l.startedAt = time.Now()
	if l.LocalAddrs == nil {
		l.LocalAddrs = LocalAddrs
	}
	if l.Hostname == "" {
		l.Hostname, _ = os.Hostname()
	}
	l.client = &http.Client{Timeout: l.PeerReportTimeout}
	if l.fetchPeer == nil {
		l.fetchPeer = l.fetchPeerReport
	}
	go l.discoverLoop(ctx)
	go l.locationsLoop(ctx)
	go l.Pinger.Run(ctx)
}

func (l *Live) discoverLoop(ctx context.Context) {
	ticker := time.NewTicker(l.DiscoverEvery)
	defer ticker.Stop()
	for {
		l.discover(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Live) discover(ctx context.Context) {
	timeout := 10 * time.Second
	if len(l.StaticPeers) > 0 {
		// A fallback exists, so don't leave the mesh waiting on the API.
		timeout = 4 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var instances []datum.Instance
	err := errNoDiscovery
	source := DiscoveryAPI
	if l.API != nil {
		instances, err = l.API.ListInstances(ctx, l.Workload)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil && l.API != nil && (l.discoverErr == nil || l.discoverErr.Error() != err.Error()) {
		slog.Warn("instance discovery through the Datum Cloud API failed", "error", err, "staticPeers", len(l.StaticPeers))
	}
	if err != nil && len(l.StaticPeers) > 0 {
		instances, err, source = l.StaticPeers, nil, DiscoveryStatic
	}
	if err != nil {
		l.discoverErr = err
		return
	}
	if source != l.discovery {
		slog.Info("discovering instances", "source", source, "count", len(instances))
	}
	l.discoverErr = nil
	l.discovered = true
	l.discovery = source
	l.instances = instances

	self := l.SelfName
	if self == "" {
		self = FindSelf(instances, l.LocalAddrs(), l.Hostname)
	}
	if self != l.self {
		slog.Info("identified self", "instance", self)
	}
	l.self = self

	peers := make([]Peer, 0, len(instances))
	for _, inst := range instances {
		if inst.Name == self {
			l.selfLocation = inst.Location
			continue
		}
		if inst.PrivateIP.IsValid() {
			peers = append(peers, Peer{Name: inst.Name, Addr: inst.PrivateIP})
		}
	}
	l.Pinger.SetPeers(peers)
}

func (l *Live) locationsLoop(ctx context.Context) {
	ticker := time.NewTicker(l.LocationsEvery)
	defer ticker.Stop()
	if l.API == nil {
		return
	}
	for {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		places, err := l.API.ListLocations(lctx)
		cancel()
		if err != nil {
			// The built-in table covers every published location.
			slog.Debug("location lookup failed, using built-in table", "error", err)
		} else {
			l.Directory.Update(places)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Local returns this instance's own measurements.
func (l *Live) Local() LocalReport {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return LocalReport{
		Name:      l.self,
		Location:  l.selfLocation,
		StartedAt: l.startedAt,
		Peers:     l.Pinger.Stats(),
		Exchanges: l.Pinger.Exchanges(MaxFeedExchanges),
	}
}

// View assembles the fleet view, reusing a recent result so many viewers do
// not multiply traffic on the private network.
func (l *Live) View(ctx context.Context) View {
	l.viewMu.Lock()
	defer l.viewMu.Unlock()
	if !l.cachedAt.IsZero() && time.Since(l.cachedAt) < l.CacheFor {
		return l.cached
	}

	l.mu.RLock()
	instances := l.instances
	self := l.self
	discoverErr := l.discoverErr
	discovered := l.discovered
	discovery := l.discovery
	l.mu.RUnlock()

	reports := map[string]*LocalReport{}
	var rmu sync.Mutex
	var wg sync.WaitGroup
	for _, inst := range instances {
		if inst.Name == self {
			local := l.Local()
			reports[inst.Name] = &local
			continue
		}
		if !inst.PrivateIP.IsValid() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.fetchPeer(ctx, inst)
			if err != nil || r == nil {
				return
			}
			rmu.Lock()
			reports[inst.Name] = r
			rmu.Unlock()
		}()
	}
	wg.Wait()

	v := Assemble(instances, l.Directory, self, reports, time.Now())
	if l.activity == nil {
		l.activity = newActivityLog()
	}
	l.activity.observe(v.Instances, v.GeneratedAt)
	v.Activity = l.activity.recent()
	v.Mode = "live"
	v.Discovery = discovery
	v.Project = l.Project
	v.Workload = l.Workload
	switch {
	case !discovered && discoverErr != nil:
		v.Notice = "Connecting to the Datum Cloud API to discover instances…"
	case !discovered:
		v.Notice = "Discovering instances…"
	case discoverErr != nil:
		v.Notice = "Showing the last known fleet while reconnecting to the Datum Cloud API."
	}

	l.cached = v
	l.cachedAt = time.Now()
	return v
}

func (l *Live) fetchPeerReport(ctx context.Context, inst datum.Instance) (*LocalReport, error) {
	ctx, cancel := context.WithTimeout(ctx, l.PeerReportTimeout)
	defer cancel()
	u := "http://" + net.JoinHostPort(inst.PrivateIP.String(), strconv.Itoa(l.Port)) + "/mesh/local"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{status: resp.Status}
	}
	var r LocalReport
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, err
	}
	// The peer may not have identified itself yet; discovery knows its name.
	r.Name = inst.Name
	return &r, nil
}

var errNoDiscovery = errors.New("no discovery source configured")

// ParseStaticPeers reads MESH_PEERS: comma-separated location=address
// entries such as "us-central-1=fd20:0:12::2:0:0,us-east-1=fd20:0:12:1:0:1::".
// Names are derived from the location and the entry's position within it, so
// every instance given the same list agrees on every peer's name.
func ParseStaticPeers(spec string) ([]datum.Instance, error) {
	var out []datum.Instance
	perLocation := map[string]int{}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		location, address, ok := strings.Cut(entry, "=")
		location, address = strings.TrimSpace(location), strings.TrimSpace(address)
		if !ok || location == "" {
			return nil, fmt.Errorf("MESH_PEERS entry %q: want location=address", entry)
		}
		prefix, ok := datum.ParseAddress(address)
		if !ok {
			return nil, fmt.Errorf("MESH_PEERS entry %q: invalid address", entry)
		}
		out = append(out, datum.Instance{
			Name:      fmt.Sprintf("%s-%d", location, perLocation[location]),
			Location:  location,
			PrivateIP: prefix.Addr(),
			Prefix:    prefix,
			Available: true,
		})
		perLocation[location]++
	}
	return out, nil
}

type httpError struct{ status string }

func (e *httpError) Error() string { return "peer report: " + e.status }
