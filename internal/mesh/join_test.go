package mesh

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/datum"
	"github.com/datum-labs/compute-network-demo/internal/geo"
)

const (
	fixtureDFW  = "gp-http-proof-default-us-central-1-0"
	fixtureDFW2 = "xcheck-dfw-us-central-1-0"
	fixtureIAD  = "xcheck-iad-us-east-1-0"
)

// instanceTimes places one Instance's lifecycle in time for a test case.
type instanceTimes struct {
	created time.Time
	// available is the Available condition's transition. A zero value stands
	// for an Instance that has never been available.
	available time.Time
	// starting leaves the Available condition unresolved, as the API reports
	// an Instance that is still coming up.
	starting bool
}

// fixtureWithTimes rewrites the recorded API payload's timestamps so a case can
// place each Instance's creation and availability where it needs them, while
// the shape the parser reads stays the one the real API returns. Instances
// missing from times are dropped from the payload.
func fixtureWithTimes(t *testing.T, times map[string]instanceTimes) []datum.Instance {
	t.Helper()
	data, err := os.ReadFile("../datum/testdata/instances.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	items, ok := doc["items"].([]any)
	if !ok {
		t.Fatalf("fixture has no items")
	}
	kept := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("fixture item is not an object")
		}
		meta := item["metadata"].(map[string]any)
		name := meta["name"].(string)
		when, ok := times[name]
		if !ok {
			continue
		}
		meta["creationTimestamp"] = rfc3339(when.created)
		for _, raw := range item["status"].(map[string]any)["conditions"].([]any) {
			cond := raw.(map[string]any)
			if cond["type"] != "Available" {
				continue
			}
			cond["status"] = "True"
			if when.starting {
				cond["status"] = "Unknown"
			}
			cond["lastTransitionTime"] = rfc3339(when.available)
		}
		kept = append(kept, item)
	}
	if len(kept) != len(times) {
		t.Fatalf("fixture matched %d of %d requested Instances", len(kept), len(times))
	}
	doc["items"] = kept
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	insts, err := datum.ParseInstanceList(out)
	if err != nil {
		t.Fatal(err)
	}
	return insts
}

// rfc3339 renders a timestamp as the API does, leaving an unset one empty.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// allUp lists every directed pair between names, as a fleet whose traffic is
// flowing everywhere reports it.
func allUp(names ...string) []string {
	var out []string
	for _, from := range names {
		for _, to := range names {
			if from != to {
				out = append(out, from+">"+to)
			}
		}
	}
	return out
}

// upExcept is allUp without the traffic reaching one Instance, which is how the
// fleet reports an Instance that has not joined the private network yet.
func upExcept(unreachable string, names ...string) []string {
	var out []string
	for _, pair := range allUp(names...) {
		if pair[len(pair)-len(unreachable)-1:] != ">"+unreachable {
			out = append(out, pair)
		}
	}
	return out
}

// reportsFor turns a set of flowing pairs into the per-Instance measurements
// the serving Instance collects from its peers.
func reportsFor(insts []datum.Instance, up []string, now time.Time) map[string]*LocalReport {
	flowing := make(map[string]bool, len(up))
	for _, pair := range up {
		flowing[pair] = true
	}
	out := make(map[string]*LocalReport, len(insts))
	for _, from := range insts {
		r := &LocalReport{Name: from.Name, StartedAt: now.Add(-time.Minute)}
		for _, to := range insts {
			if to.Name == from.Name {
				continue
			}
			p := PeerStats{Name: to.Name, Address: to.PrivateIP.String()}
			if flowing[from.Name+">"+to.Name] {
				p.RTTMs, p.SuccessRate, p.Attempts, p.Messages = 2, 1, 5, 10
				p.LastSuccess = now
			}
			r.Peers = append(r.Peers, p)
		}
		out[from.Name] = r
	}
	return out
}

// joinStep is one poll of the fleet: who exists, how they are placed in time,
// and whose traffic is flowing.
type joinStep struct {
	at    time.Time
	times map[string]instanceTimes
	up    []string
}

func joinOf(v View, name string) float64 {
	for _, iv := range v.Instances {
		if iv.Name == name {
			return iv.JoinMs
		}
	}
	return -1
}

func TestJoinTrackerMeasuresJoinTime(t *testing.T) {
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	weeksAgo := base.Add(-9 * 24 * time.Hour)
	settled := map[string]instanceTimes{
		fixtureDFW:  {created: weeksAgo, available: weeksAgo.Add(20 * time.Second)},
		fixtureDFW2: {created: weeksAgo, available: weeksAgo.Add(20 * time.Second)},
	}

	cases := []struct {
		name   string
		steps  []joinStep
		target string
		want   float64
	}{
		{
			name:   "new Instance measured from availability to full reachability",
			target: fixtureIAD,
			want:   6000,
			steps: []joinStep{
				{at: base, times: settled, up: allUp(fixtureDFW, fixtureDFW2)},
				{
					at: base.Add(5 * time.Second),
					times: map[string]instanceTimes{
						fixtureDFW:  settled[fixtureDFW],
						fixtureDFW2: settled[fixtureDFW2],
						fixtureIAD:  {created: base.Add(time.Second), available: base.Add(3 * time.Second)},
					},
					up: upExcept(fixtureIAD, fixtureDFW, fixtureDFW2, fixtureIAD),
				},
				{
					at: base.Add(9 * time.Second),
					times: map[string]instanceTimes{
						fixtureDFW:  settled[fixtureDFW],
						fixtureDFW2: settled[fixtureDFW2],
						fixtureIAD:  {created: base.Add(time.Second), available: base.Add(3 * time.Second)},
					},
					up: allUp(fixtureDFW, fixtureDFW2, fixtureIAD),
				},
			},
		},
		{
			name:   "in-place restart re-arms the clock",
			target: fixtureIAD,
			want:   6000,
			steps: []joinStep{
				{
					at: base,
					times: map[string]instanceTimes{
						fixtureDFW:  settled[fixtureDFW],
						fixtureDFW2: settled[fixtureDFW2],
						fixtureIAD:  {created: weeksAgo, available: weeksAgo.Add(15 * time.Second)},
					},
					up: allUp(fixtureDFW, fixtureDFW2, fixtureIAD),
				},
				{
					at: base.Add(10 * time.Second),
					times: map[string]instanceTimes{
						fixtureDFW:  settled[fixtureDFW],
						fixtureDFW2: settled[fixtureDFW2],
						// Restarted in place: availability moved forward while
						// the creation timestamp stayed weeks back.
						fixtureIAD: {created: weeksAgo, available: base.Add(8 * time.Second)},
					},
					up: upExcept(fixtureIAD, fixtureDFW, fixtureDFW2, fixtureIAD),
				},
				{
					at: base.Add(14 * time.Second),
					times: map[string]instanceTimes{
						fixtureDFW:  settled[fixtureDFW],
						fixtureDFW2: settled[fixtureDFW2],
						fixtureIAD:  {created: weeksAgo, available: base.Add(8 * time.Second)},
					},
					up: allUp(fixtureDFW, fixtureDFW2, fixtureIAD),
				},
			},
		},
		{
			name:   "stale derived join time is dropped",
			target: fixtureDFW,
			want:   0,
			steps: []joinStep{{
				at: base,
				times: map[string]instanceTimes{
					// Nine days between creation and availability: an in-place
					// update, not a join.
					fixtureDFW:  {created: base.Add(-10 * 24 * time.Hour), available: base.Add(-24 * time.Hour)},
					fixtureDFW2: settled[fixtureDFW2],
				},
				up: allUp(fixtureDFW, fixtureDFW2),
			}},
		},
		{
			name:   "plausible derived join time is kept",
			target: fixtureDFW,
			want:   20000,
			steps: []joinStep{{
				at: base,
				times: map[string]instanceTimes{
					fixtureDFW:  {created: base.Add(-time.Hour), available: base.Add(-time.Hour + 20*time.Second)},
					fixtureDFW2: settled[fixtureDFW2],
				},
				up: allUp(fixtureDFW, fixtureDFW2),
			}},
		},
		{
			name:   "Instance already reachable at startup is never measured",
			target: fixtureIAD,
			want:   0,
			steps: []joinStep{
				{
					at: base,
					times: map[string]instanceTimes{
						fixtureDFW:  settled[fixtureDFW],
						fixtureDFW2: settled[fixtureDFW2],
						// Stale derived value, and no join to observe.
						fixtureIAD: {created: weeksAgo, available: base.Add(-24 * time.Hour)},
					},
					up: allUp(fixtureDFW, fixtureDFW2, fixtureIAD),
				},
				{
					at: base.Add(30 * time.Second),
					times: map[string]instanceTimes{
						fixtureDFW:  settled[fixtureDFW],
						fixtureDFW2: settled[fixtureDFW2],
						fixtureIAD:  {created: weeksAgo, available: base.Add(-24 * time.Hour)},
					},
					up: allUp(fixtureDFW, fixtureDFW2, fixtureIAD),
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tracker := newJoinTracker()
			var v View
			for _, s := range c.steps {
				insts := fixtureWithTimes(t, s.times)
				v = Assemble(insts, geo.NewDirectory(), fixtureDFW, reportsFor(insts, s.up, s.at), s.at)
				tracker.observe(&v, insts, s.at)
			}
			if got := joinOf(v, c.target); got != c.want {
				t.Fatalf("JoinMs for %s = %v, want %v", c.target, got, c.want)
			}
		})
	}
}

func TestJoinReadyEventCarriesMeasuredJoin(t *testing.T) {
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	weeksAgo := base.Add(-9 * 24 * time.Hour)
	old := instanceTimes{created: weeksAgo, available: weeksAgo.Add(20 * time.Second)}
	joining := instanceTimes{created: base.Add(time.Second), available: base.Add(3 * time.Second)}

	steps := []joinStep{
		{at: base, times: map[string]instanceTimes{fixtureDFW: old, fixtureDFW2: old}, up: allUp(fixtureDFW, fixtureDFW2)},
		{
			at:    base.Add(5 * time.Second),
			times: map[string]instanceTimes{fixtureDFW: old, fixtureDFW2: old, fixtureIAD: joining},
			up:    upExcept(fixtureIAD, fixtureDFW, fixtureDFW2, fixtureIAD),
		},
		{
			at:    base.Add(9 * time.Second),
			times: map[string]instanceTimes{fixtureDFW: old, fixtureDFW2: old, fixtureIAD: joining},
			up:    allUp(fixtureDFW, fixtureDFW2, fixtureIAD),
		},
		{
			at:    base.Add(13 * time.Second),
			times: map[string]instanceTimes{fixtureDFW: old, fixtureDFW2: old, fixtureIAD: joining},
			up:    allUp(fixtureDFW, fixtureDFW2, fixtureIAD),
		},
	}

	tracker := newJoinTracker()
	log := newActivityLog()
	var ready []Activity
	for i, s := range steps {
		insts := fixtureWithTimes(t, s.times)
		v := Assemble(insts, geo.NewDirectory(), fixtureDFW, reportsFor(insts, s.up, s.at), s.at)
		tracker.observe(&v, insts, s.at)
		log.observe(v.Instances, s.at)

		ready = nil
		for _, e := range log.recent() {
			if e.Type == ActivityReady && e.Instance == fixtureIAD {
				ready = append(ready, e)
			}
		}
		// The join is still being timed, so the Instance has not joined yet.
		if i < 2 && len(ready) != 0 {
			t.Fatalf("step %d: ready events = %+v, want none while the join is being measured", i, ready)
		}
	}

	if len(ready) != 1 {
		t.Fatalf("ready events = %+v, want exactly one per start", ready)
	}
	if ready[0].JoinMs != 6000 {
		t.Errorf("ready JoinMs = %v, want 6000", ready[0].JoinMs)
	}
	if !ready[0].At.Equal(base.Add(9 * time.Second)) {
		t.Errorf("ready at = %s, want the moment the measurement stopped", ready[0].At)
	}
}
