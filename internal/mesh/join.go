package mesh

import (
	"sync"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/datum"
)

// maxFallbackJoin bounds the join time derived from the API's own timestamps.
// The platform updates an Instance in place without resetting its creation
// timestamp, so availability minus creation reads as days for an Instance that
// in fact rejoined the network in seconds. Anything beyond an hour is not a
// join time, and the page reads better with no number than with a wrong one.
const maxFallbackJoin = time.Hour

// maxJoinMeasurement bounds how long the fleet waits for a new Instance to
// become reachable everywhere. Past it the join is no longer news, so the feed
// gives up and reports the Instance as joined rather than withholding it.
const maxJoinMeasurement = 2 * time.Minute

// joinTracker times how long each Instance takes to become reachable by every
// one of its running peers, which is the number this demo exists to show. The
// platform exposes no per-start timestamp — an Instance restarted in place
// keeps the creation timestamp it was first given — so the fleet measures the
// join from its own observations instead of deriving it from the API.
type joinTracker struct {
	mu     sync.Mutex
	starts map[string]*joinStart
	primed bool
}

// joinStart is one run of an Instance: the clock that run started from, and the
// result once every running peer reported it reachable.
type joinStart struct {
	// availableAt is the availability transition this run was armed from. A
	// later transition means the Instance started again and the clock re-arms.
	availableAt time.Time
	armedAt     time.Time
	// measuring is true while the fleet is still waiting for the Instance to
	// turn up in every running peer's measurements.
	measuring bool
	joinMs    float64
}

func newJoinTracker() *joinTracker {
	return &joinTracker{starts: map[string]*joinStart{}}
}

// observe advances every Instance's measurement against the fleet view just
// assembled and fills in the join times the page shows. A measured join
// replaces whatever was derived from the API's timestamps, and an Instance
// still being timed is marked as such so its ready event waits for the result.
func (t *joinTracker) observe(v *View, instances []datum.Instance, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	availableAt := make(map[string]time.Time, len(instances))
	for _, inst := range instances {
		availableAt[inst.Name] = inst.AvailableAt
	}
	running := make(map[string]bool, len(v.Instances))
	for _, iv := range v.Instances {
		running[iv.Name] = iv.Status == StatusRunning
	}
	reachedBy := peerReach(v, running)

	// Instances already running when the fleet first looked were not seen
	// joining, so they only ever carry the derived fallback.
	baseline := !t.primed
	t.primed = true

	alive := make(map[string]bool, len(v.Instances))
	for i := range v.Instances {
		iv := &v.Instances[i]
		alive[iv.Name] = true
		available := availableAt[iv.Name]

		st := t.starts[iv.Name]
		switch {
		case st == nil && baseline:
			st = &joinStart{availableAt: available}
		case st == nil:
			// A name the fleet has not seen before is a new start. Its
			// availability is the clock when the API reports one; otherwise
			// the Instance is still coming up and this is as early as the
			// fleet could know about it.
			armed := available
			if armed.IsZero() {
				armed = now
			}
			st = &joinStart{availableAt: available, armedAt: armed, measuring: true}
		case available.After(st.availableAt):
			// Availability moved forward under a name the fleet already knew:
			// the Instance started again and is joining the network afresh.
			st = &joinStart{availableAt: available, armedAt: available, measuring: true}
		}
		t.starts[iv.Name] = st

		peers := runningPeers(running, iv.Name)
		switch {
		case !st.measuring:
		case iv.Status == StatusStopping:
			// There is no join to report for an Instance on its way out.
			st.measuring = false
		case iv.Status == StatusRunning && peers == 0:
			// A lone Instance is up but has nobody to be reachable by, so
			// there is nothing for the fleet to time.
			st.measuring = false
		case peers > 0 && reachedBy[iv.Name] >= peers:
			st.measuring = false
			if elapsed := now.Sub(st.armedAt); elapsed > 0 {
				st.joinMs = float64(elapsed.Milliseconds())
			}
		case now.Sub(st.armedAt) > maxJoinMeasurement:
			st.measuring = false
		}

		if st.joinMs > 0 {
			iv.JoinMs = st.joinMs
		}
		iv.JoinPending = st.measuring
	}

	// A workload that scales for hours should not grow a record of every
	// Instance it ever ran.
	for name := range t.starts {
		if !alive[name] {
			delete(t.starts, name)
		}
	}
}

// peerReach counts, per Instance, how many running peers are getting traffic
// through to it. Only the inbound direction counts: the claim the demo makes is
// that a new Instance is reachable by the rest of the fleet, which is something
// only its peers can report.
func peerReach(v *View, running map[string]bool) map[string]int {
	seen := map[string]map[string]bool{}
	for _, e := range v.Edges {
		if !running[e.From] || e.From == e.To {
			continue
		}
		if e.State != EdgeUp && e.State != EdgeDegraded {
			continue
		}
		if seen[e.To] == nil {
			seen[e.To] = map[string]bool{}
		}
		seen[e.To][e.From] = true
	}
	out := make(map[string]int, len(seen))
	for to, froms := range seen {
		out[to] = len(froms)
	}
	return out
}

// runningPeers counts the Instances that name has to be reachable by. Peers
// that are starting or draining are left out: an Instance cannot be held to
// account for traffic that nothing is sending yet.
func runningPeers(running map[string]bool, name string) int {
	n := 0
	for peer, up := range running {
		if peer != name && up {
			n++
		}
	}
	return n
}
