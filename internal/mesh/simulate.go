package mesh

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"net/netip"
	"sync"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/datum"
	"github.com/datum-labs/compute-network-demo/internal/geo"
)

// simPolicy is how one location answers demand: a floor of always-on
// Instances, and a repeating staircase of targets on top of it. Each location
// gets its own step length and its own place in the cycle, so the fleet never
// scales in lockstep — watching one region settle while another grows is the
// whole point of showing three.
type simPolicy struct {
	location string
	// targets is the Instance count the location is driven to, one entry per
	// step, repeating. Its smallest entry is the floor.
	targets []int
	step    time.Duration
	// offset moves the location along its own cycle, so decisions land at
	// different moments across the fleet.
	offset time.Duration
	// baseUptime is how long the floor Instances have been running when the
	// demo starts; a fleet that looks brand new looks like a test.
	baseUptime time.Duration
}

// simEpoch starts the cycles well before the process did, so a freshly
// started simulator can replay the last couple of minutes of scaling and open
// with an activity feed rather than an empty panel.
const simEpoch = 30 * time.Minute

// activityBackfill is how much of that history is replayed on the first view.
const activityBackfill = 150 * time.Second

// activityStep is how finely the replay walks. Scaling decisions are seconds
// apart at the closest, so a second's resolution loses nothing.
const activityStep = time.Second

// Simulator fakes a multi-region fleet with latencies derived from distance
// and a workload that scales itself, so the page can be developed and
// presented without cloud access.
//
// Everything it reports is a pure function of the time asked for: the fleet,
// each Instance's lifecycle and the events that narrate them are derived from
// the elapsed cycle and stable hashes of instance names rather than from
// random numbers, so a poll that arrives late sees the same story as one that
// arrives on time.
type Simulator struct {
	Workload  string
	Directory *geo.Directory
	// Churn enables scaling. With it off every location sits at its floor,
	// which is what tests and a scripted walkthrough want.
	Churn  bool
	Faults bool

	start    time.Time
	policies []simPolicy
	// faultA and faultB name the one replica pair that breaks when faults are
	// enabled.
	faultA, faultB string
	// now is the clock, so tests can walk a whole scaling cycle in an instant.
	now func() time.Time

	mu       sync.Mutex
	counters map[string]*simCounter
	lastTick time.Time
	// feed holds recent exchanges per sending instance, so the page shows a
	// live feed rather than a static list.
	feed          map[string][]Exchange
	activity      *activityLog
	observedUntil time.Time
}

type simCounter struct {
	messages float64
	bytes    float64
}

// NewSimulator returns the three launch locations, each scaling on its own
// cycle: Dallas breathes widely, San Jose follows a shorter cycle on a
// different phase, and Ashburn stays steady enough to read as a reference.
func NewSimulator(workload string, dir *geo.Directory, churn, faults bool) *Simulator {
	s := &Simulator{
		Workload:  workload,
		Directory: dir,
		Churn:     churn,
		Faults:    faults,
		start:     time.Now(),
		now:       time.Now,
		counters:  map[string]*simCounter{},
		feed:      map[string][]Exchange{},
		activity:  newActivityLog(),
		policies: []simPolicy{
			{location: "us-central-1", targets: []int{2, 3, 4, 5, 4, 3, 2, 2}, step: 11 * time.Second, baseUptime: 3*time.Hour + 12*time.Minute},
			{location: "us-east-1", targets: []int{2, 2, 3, 2, 2, 2}, step: 21 * time.Second, offset: 47 * time.Second, baseUptime: 3*time.Hour + 11*time.Minute},
			{location: "us-west-1", targets: []int{2, 3, 4, 3, 2}, step: 13 * time.Second, offset: 29 * time.Second, baseUptime: 2*time.Hour + 48*time.Minute},
		},
	}
	s.observedUntil = s.start.Add(-activityBackfill)
	// One replica pair, not a whole region: with three locations a dark
	// region-to-region link would read as an outage rather than a fault. Both
	// ends sit on their location's floor so the fault is always visible.
	s.faultA = s.instanceName("us-central-1", 1)
	s.faultB = s.instanceName("us-east-1", 1)
	return s
}

func (s *Simulator) instanceName(location string, slot int) string {
	return fmt.Sprintf("%s-%s-%d", s.Workload, location, slot)
}

// phase is where the cycles stand at t.
func (s *Simulator) phase(t time.Time) time.Duration { return simEpoch + t.Sub(s.start) }

// at converts a point in a cycle back to wall-clock time.
func (s *Simulator) at(p time.Duration) time.Time { return s.start.Add(p - simEpoch) }

func floorTarget(targets []int) int {
	least := targets[0]
	for _, v := range targets {
		least = min(least, v)
	}
	return least
}

func ceilTarget(targets []int) int {
	most := targets[0]
	for _, v := range targets {
		most = max(most, v)
	}
	return most
}

// slotWindow reports whether the slot at index i is wanted at p, when that was
// decided, and when the decision before it was taken. Slots above the floor
// come and go once per cycle, so both are always within a couple of periods.
func slotWindow(pol simPolicy, i int, p time.Duration) (wanted bool, since, before time.Duration) {
	n := len(pol.targets)
	decision := func(k int) time.Duration { return time.Duration(k)*pol.step - pol.offset }
	want := func(k int) bool { return pol.targets[((k%n)+n)%n] > i }

	k := int(math.Floor(float64(p+pol.offset) / float64(pol.step)))
	wanted = want(k)
	edges := make([]int, 0, 2)
	for j := k; j > k-3*n && len(edges) < 2; j-- {
		if want(j) != want(j-1) {
			edges = append(edges, j)
		}
	}
	switch len(edges) {
	case 0:
		return wanted, decision(k - 3*n), decision(k - 3*n)
	case 1:
		return wanted, decision(edges[0]), decision(edges[0] - n)
	default:
		return wanted, decision(edges[0]), decision(edges[1])
	}
}

// How long an Instance takes to get going, derived from its name so every
// report of it agrees. Booting is the machine coming up; joining is what
// happens next, and is the number the demo is really about: the moment the
// Instance can be reached by every peer over the private network, with nothing
// configured.
func bootFor(name string) time.Duration {
	return time.Duration((2.8 + 1.6*hashFraction(name+"boot")) * float64(time.Second))
}

func joinFor(name string) time.Duration {
	return time.Duration((0.6 + 1.1*hashFraction(name+"join")) * float64(time.Second))
}

// drainFor is how long an Instance keeps serving while it winds down, so the
// map shows it leaving rather than blinking out.
func drainFor(name string) time.Duration {
	return time.Duration((3.5 + 2.0*hashFraction(name+"drain")) * float64(time.Second))
}

// SimulatedRTT estimates round-trip time between two places: light in fibre
// covers about 200 km per millisecond, routes run longer than the great
// circle, and each end adds a little processing.
func SimulatedRTT(a, b geo.Place) float64 {
	const routeFactor = 1.45
	km := geo.DistanceKm(a, b)
	// Replicas in one location share a fabric, so what they measure is
	// switching and scheduling rather than distance.
	if km < 1 {
		return 0.3
	}
	return 2*km/200*routeFactor + 2.5
}

// instance builds one Instance of the fleet as it stands at t.
func (s *Simulator) instance(pol simPolicy, slot, address int, started time.Time, stoppingAt time.Time, t time.Time) datum.Instance {
	name := s.instanceName(pol.location, slot)
	addr := netip.MustParseAddr(fmt.Sprintf("fd20:0:f::%x:0:0", address))
	join := joinFor(name)
	ready := started.Add(bootFor(name) + join)
	return datum.Instance{
		Name:        name,
		Location:    pol.location,
		PrivateIP:   addr,
		Prefix:      netip.PrefixFrom(addr, 96),
		Available:   !t.Before(ready),
		Stopping:    !stoppingAt.IsZero(),
		StoppingAt:  stoppingAt,
		CreatedAt:   started,
		AvailableAt: ready,
		JoinMs:      float64(join.Milliseconds()),
	}
}

// fleetAt returns the fleet as it stands at t, newest scaling decisions
// included.
func (s *Simulator) fleetAt(t time.Time) []datum.Instance {
	p := s.phase(t)
	var out []datum.Instance
	for pi, pol := range s.policies {
		floor := floorTarget(pol.targets)
		for slot := 0; slot < floor; slot++ {
			// Staggered so the floor does not look like it was stamped out in
			// one go.
			started := s.start.Add(-(pol.baseUptime + time.Duration(slot)*17*time.Minute))
			out = append(out, s.instance(pol, slot, pi*16+slot+1, started, time.Time{}, t))
		}
		if !s.Churn {
			continue
		}
		for slot := floor; slot < ceilTarget(pol.targets); slot++ {
			wanted, since, before := slotWindow(pol, slot, p)
			if wanted {
				out = append(out, s.instance(pol, slot, pi*16+slot+1, s.at(since), time.Time{}, t))
				continue
			}
			// Still draining: it keeps its address and its traffic winds down.
			if p-since < drainFor(s.instanceName(pol.location, slot)) {
				out = append(out, s.instance(pol, slot, pi*16+slot+1, s.at(before), s.at(since), t))
			}
		}
	}
	return out
}

func (s *Simulator) faulty(a, b string) bool {
	if !s.Faults {
		return false
	}
	pair := map[string]bool{a: true, b: true}
	return pair[s.faultA] && pair[s.faultB]
}

// Local returns the simulated self's report.
func (s *Simulator) Local() LocalReport {
	pol := s.policies[0]
	return LocalReport{
		Name:      s.instanceName(pol.location, 0),
		Location:  pol.location,
		StartedAt: s.start.Add(-pol.baseUptime),
	}
}

// View returns the simulated fleet.
func (s *Simulator) View(_ context.Context) View {
	now := s.now()
	instances := s.fleetAt(now)

	s.mu.Lock()
	defer s.mu.Unlock()
	dt := 0.0
	if s.lastTick.IsZero() {
		// Backfill so the first page load already shows a feed rather than
		// waiting for the next poll.
		dt = 6
	} else {
		dt = now.Sub(s.lastTick).Seconds()
	}
	s.lastTick = now

	// Traffic only counts from the moment both ends are reachable by each
	// other, and tapers off as either end drains.
	active := map[string]time.Duration{}
	drain := map[string]float64{}
	token := map[string]string{}
	for _, inst := range instances {
		if inst.Available {
			active[inst.Name] = now.Sub(inst.AvailableAt)
		}
		drain[inst.Name] = 1
		if inst.Stopping {
			window := drainFor(inst.Name)
			drain[inst.Name] = math.Max(0, math.Min(1, (window-now.Sub(inst.StoppingAt)).Seconds()/window.Seconds()))
		}
		// Slots are reused as a location scales in and out, so a counter is
		// tied to this run of the Instance rather than to its name.
		token[inst.Name] = fmt.Sprintf("%s@%d", inst.Name, inst.CreatedAt.UnixNano())
	}

	reports := map[string]*LocalReport{}
	live := map[string]bool{}
	for _, from := range instances {
		fp, _ := s.Directory.Lookup(from.Location)
		r := &LocalReport{Name: from.Name, Location: from.Location, StartedAt: from.CreatedAt}
		for _, to := range instances {
			if to.Name == from.Name {
				continue
			}
			tp, _ := s.Directory.Lookup(to.Location)
			key := token[from.Name] + ">" + token[to.Name]
			live[key] = true
			// Both ends have to be up before the pair has anything to report.
			youngest := math.Min(active[from.Name].Seconds(), active[to.Name].Seconds())
			c := s.counters[key]
			if c == nil {
				// Seed long-running pairs with the traffic they would have
				// exchanged so far, at one round trip every two seconds.
				c = &simCounter{messages: math.Max(0, youngest), bytes: math.Max(0, youngest) * 190}
				s.counters[key] = c
			}
			stats := PeerStats{Name: to.Name, Address: to.PrivateIP.String()}
			broken := s.faulty(from.Name, to.Name)
			taper := math.Min(drain[from.Name], drain[to.Name])
			switch {
			case !from.Available || !to.Available:
				// Not yet reachable.
			case broken:
				stats.Attempts = int64(youngest / 2)
				stats.SuccessRate = 0
			default:
				c.messages += dt * taper
				c.bytes += dt * 190 * taper
				base := SimulatedRTT(fp, tp)
				// Gentle, smooth wobble so numbers feel alive without jumping.
				wobble := 1 + 0.03*math.Sin(float64(now.UnixMilli())/4000+float64(len(key)))
				stats.RTTMs = math.Round(base*wobble*10) / 10
				stats.SuccessRate = 1
				stats.Attempts = int64(youngest/2) + 1
				stats.Messages = int64(c.messages)
				stats.Bytes = int64(c.bytes)
				stats.LastSuccess = now
			}
			// A draining pair thins out rather than stopping dead.
			if from.Available && to.Available && taper > 0.45 {
				s.recordExchanges(from.Name, to.Name, now, dt, stats.RTTMs, !broken)
			}
			r.Peers = append(r.Peers, stats)
		}
		r.Exchanges = s.feed[from.Name]
		reports[from.Name] = r
	}
	s.forget(live, instances)

	self := s.instanceName(s.policies[0].location, 0)
	v := Assemble(instances, s.Directory, self, reports, now)
	v.Mode = "simulate"
	v.Discovery = DiscoverySimulated
	v.Workload = s.Workload
	v.Project = "demo"
	v.Activity = s.narrate(now)
	return v
}

// narrate walks the activity log up to now. The fleet is a pure function of
// time, so the walk can start before the process did: the page opens with the
// last couple of minutes of scaling already in the feed, and a poll that
// arrives late still sees every event in between.
func (s *Simulator) narrate(now time.Time) []Activity {
	for t := s.observedUntil; !t.After(now); t = t.Add(activityStep) {
		s.activity.observe(s.instanceViews(t), t)
		s.observedUntil = t.Add(activityStep)
	}
	return s.activity.recent()
}

// instanceViews is the fleet at t, reduced to what the activity log reads.
func (s *Simulator) instanceViews(t time.Time) []InstanceView {
	instances := s.fleetAt(t)
	out := make([]InstanceView, 0, len(instances))
	for _, inst := range instances {
		place, _ := s.Directory.Lookup(inst.Location)
		out = append(out, InstanceView{
			Name:     inst.Name,
			Location: inst.Location,
			City:     place.City,
			Status:   InstanceStatus(inst),
			JoinMs:   inst.JoinMs,
		})
	}
	return out
}

// forget drops the state of Instances and pairs that are no longer in the
// fleet, so a workload that scales for hours does not grow a map of every
// Instance it ever ran.
func (s *Simulator) forget(live map[string]bool, instances []datum.Instance) {
	for key := range s.counters {
		if !live[key] {
			delete(s.counters, key)
		}
	}
	present := make(map[string]bool, len(instances))
	for _, inst := range instances {
		present[inst.Name] = true
	}
	for name := range s.feed {
		if !present[name] {
			delete(s.feed, name)
		}
	}
}

// recordExchanges adds the messages this pair would have exchanged during the
// window that just passed. Each pair keeps its own offset in the two-second
// cycle, so the feed arrives staggered rather than in bursts.
func (s *Simulator) recordExchanges(from, to string, now time.Time, dt, rttMs float64, ok bool) {
	if dt <= 0 {
		return
	}
	const period = 2.0
	key := from + ">" + to
	phase := float64(hashFraction(key)) * period
	elapsed := now.Sub(s.start).Seconds()
	// Walk the ticks that fall inside the window (elapsed-dt, elapsed].
	for k := math.Ceil((elapsed - dt - phase) / period); ; k++ {
		at := phase + k*period
		if at > elapsed {
			break
		}
		if at <= elapsed-dt {
			continue
		}
		jitter := float64(hashFraction(key + "rtt"))
		s.feed[from] = appendWindow(s.feed[from], Exchange{
			At:    s.start.Add(time.Duration(at * float64(time.Second))),
			To:    to,
			RTTMs: math.Round(rttMs*(1+jitter*0.04)*10) / 10,
			OK:    ok,
		}, MaxFeedExchanges)
	}
}

// hashFraction maps a string to a stable value in [0,1), used to give each
// pair its own slot in the cycle.
func hashFraction(s string) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return float64(h.Sum32()%10000) / 10000
}
