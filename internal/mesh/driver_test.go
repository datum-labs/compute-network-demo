package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/datum"
)

// fakeWorkloads stands in for the Datum Cloud API: it holds one Workload and
// records every write the driver makes.
type fakeWorkloads struct {
	workload datum.Workload
	writes   [][]datum.Placement
	getErr   error
	patchErr error
	// conflictOnce makes the first write lose a race, as a second driver
	// writing at the same moment would.
	conflictOnce bool
}

func (f *fakeWorkloads) GetWorkload(_ context.Context, _ string) (*datum.Workload, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	w := f.workload
	w.Placements = append([]datum.Placement(nil), f.workload.Placements...)
	return &w, nil
}

func (f *fakeWorkloads) PatchWorkloadPlacements(_ context.Context, _ string, placements []datum.Placement, _ string) error {
	if f.conflictOnce {
		f.conflictOnce = false
		return datum.ErrConflict
	}
	if f.patchErr != nil {
		return f.patchErr
	}
	f.writes = append(f.writes, placements)
	f.workload.Placements = placements
	return nil
}

func placement(name, city string) datum.Placement {
	return datum.Placement{
		Name:             name,
		LocationSelector: map[string]any{"matchLabels": map[string]any{"topology.datum.net/city-code": city}},
		ScaleSettings:    map[string]any{"minReplicas": float64(1), "instanceManagementPolicy": "OrderedReady"},
	}
}

// twoCityWorkload is the demo's shape: one base placement per city, both ready.
func twoCityWorkload() datum.Workload {
	return datum.Workload{
		Name:            "mesh",
		ResourceVersion: "1",
		Placements:      []datum.Placement{placement("dfw", "DFW"), placement("iad", "IAD")},
		PlacementStatus: []datum.PlacementStatus{{Name: "dfw", Available: true}, {Name: "iad", Available: true}},
	}
}

// stableView is a fleet where every Instance runs and every link is up.
func stableView(self string, names ...string) View {
	v := View{Self: self}
	for _, n := range names {
		v.Instances = append(v.Instances, InstanceView{Name: n, Status: StatusRunning, Reporting: true})
	}
	for _, from := range names {
		for _, to := range names {
			if from != to {
				v.Edges = append(v.Edges, EdgeView{From: from, To: to, State: EdgeUp, SuccessRate: 1})
			}
		}
	}
	return v
}

// Instance names as the platform builds them: the workload, the placement, the
// location, and the ordinal within the placement. An extra placement's number
// therefore lands in the middle of the name, below every letter it is compared
// against.
const (
	dfwInstance      = "global-mesh-dfw-us-central-1-0"
	dfwExtraInstance = "global-mesh-dfw-2-us-central-1-0"
	iadInstance      = "global-mesh-iad-us-east-1-0"
	sjcInstance      = "global-mesh-sjc-us-west-1-0"
)

// aged stamps creation times onto a view's Instances, so an election has ages
// to compare. Instances left out keep an unknown age.
func aged(v View, at map[string]time.Time) View {
	for i := range v.Instances {
		if t, ok := at[v.Instances[i].Name]; ok {
			v.Instances[i].CreatedAt = t
		}
	}
	return v
}

// stopped marks one Instance as winding down.
func stopped(v View, name string) View {
	for i := range v.Instances {
		if v.Instances[i].Name == name {
			v.Instances[i].Status = StatusStopping
		}
	}
	return v
}

func TestOldestRunningLeadsTheFleet(t *testing.T) {
	t0 := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	hourAgo := t0.Add(-time.Hour)
	dayAgo := t0.Add(-24 * time.Hour)

	cases := []struct {
		name string
		view View
		want bool
	}{
		{
			name: "the lowest name does not lead when it is the newest Instance",
			view: aged(stableView(dfwInstance, dfwInstance, iadInstance), map[string]time.Time{
				dfwInstance: t0, iadInstance: dayAgo,
			}),
		},
		{
			name: "the oldest Instance leads",
			view: aged(stableView(iadInstance, dfwInstance, iadInstance), map[string]time.Time{
				dfwInstance: t0, iadInstance: dayAgo,
			}),
			want: true,
		},
		{
			name: "a tie on age falls to the lowest name",
			view: aged(stableView(dfwInstance, dfwInstance, iadInstance), map[string]time.Time{
				dfwInstance: dayAgo, iadInstance: dayAgo,
			}),
			want: true,
		},
		{
			name: "an Instance of an extra placement does not lead while a base Instance runs",
			view: aged(stableView(dfwExtraInstance, dfwInstance, dfwExtraInstance), map[string]time.Time{
				dfwInstance: hourAgo, dfwExtraInstance: dayAgo,
			}),
		},
		{
			name: "the base Instance leads even when an extra is older",
			view: aged(stableView(dfwInstance, dfwInstance, dfwExtraInstance), map[string]time.Time{
				dfwInstance: hourAgo, dfwExtraInstance: dayAgo,
			}),
			want: true,
		},
		{
			name: "an extra leads when it is the only Instance left running",
			view: stopped(aged(stableView(dfwExtraInstance, dfwInstance, dfwExtraInstance), map[string]time.Time{
				dfwInstance: dayAgo, dfwExtraInstance: t0,
			}), dfwInstance),
			want: true,
		},
		{
			name: "leadership passes to the next-oldest when the leader stops",
			view: stopped(aged(stableView(sjcInstance, dfwInstance, iadInstance, sjcInstance), map[string]time.Time{
				dfwInstance: dayAgo, iadInstance: t0, sjcInstance: hourAgo,
			}), dfwInstance),
			want: true,
		},
		{
			name: "an Instance whose age is unknown yields to one that has an age",
			view: aged(stableView(dfwInstance, dfwInstance, iadInstance), map[string]time.Time{
				iadInstance: t0,
			}),
		},
		{
			name: "sole Instance leads",
			view: stableView(dfwInstance, dfwInstance),
			want: true,
		},
		{
			name: "an Instance that has not identified itself never leads",
			view: stableView("", dfwInstance),
		},
		{
			name: "an Instance still starting does not lead, however old",
			view: func() View {
				v := aged(stableView(iadInstance, iadInstance), map[string]time.Time{iadInstance: t0})
				v.Instances = append([]InstanceView{{Name: dfwInstance, Status: StatusStarting, CreatedAt: dayAgo}}, v.Instances...)
				return v
			}(),
			want: true,
		},
		{
			name: "an Instance stopping does not lead, however old",
			view: func() View {
				v := aged(stableView(iadInstance, iadInstance), map[string]time.Time{iadInstance: t0})
				v.Instances = append([]InstanceView{{Name: dfwInstance, Status: StatusStopping, CreatedAt: dayAgo}}, v.Instances...)
				return v
			}(),
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := leads(c.view); got != c.want {
				t.Errorf("leads() = %v, want %v", got, c.want)
			}
		})
	}
}

// A location whose own name ends in a number must not make every Instance in it
// look like an extra: only an Instance whose base Instance is in the same fleet
// is one.
func TestExtraInstancesComeFromNumberedPlacements(t *testing.T) {
	v := stableView("", dfwInstance, dfwExtraInstance, "global-mesh-dfw-us-west-2-0")
	got := extraInstances(v.Instances)
	if len(got) != 1 || !got[dfwExtraInstance] {
		t.Fatalf("extras = %+v", got)
	}
}

// The bug this guards: scaling Dallas up adds the placement dfw-2, whose
// Instance name sorts below every Instance already running. Electing by name
// handed the fleet to that new Instance, which knew nothing of the change that
// created it and scaled again within seconds.
func TestScaleUpDoesNotMoveLeadership(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload()}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	d := newTestDriver(t, api, &now)

	ages := map[string]time.Time{
		dfwInstance: now.Add(-24 * time.Hour),
		iadInstance: now.Add(-time.Hour),
	}
	leading := aged(stableView(dfwInstance, dfwInstance, iadInstance), ages)
	takeTheFleet(t, d, &now, leading)
	d.Tick(context.Background(), leading)
	if len(api.writes) != 1 || api.writes[0][2].Name != "dfw-2" {
		t.Fatalf("writes = %+v", api.writes)
	}

	// The Instance the scale-up asked for arrives, newest of the fleet.
	now = now.Add(90 * time.Second)
	ages[dfwExtraInstance] = now
	grown := aged(stableView(dfwInstance, dfwInstance, dfwExtraInstance, iadInstance), ages)

	d.Tick(context.Background(), grown)
	if st := d.State(); !st.IsLeader {
		t.Error("the Instance that scaled the fleet lost the fleet to the Instance it created")
	}
	if len(api.writes) != 1 {
		t.Fatalf("acted inside the interval: %+v", api.writes)
	}

	// The new Instance runs the same code against the same view, and has none
	// of the leader's interval or settled state to hold it back.
	newcomer := &fakeWorkloads{workload: api.workload}
	fresh := newTestDriver(t, newcomer, &now)
	fresh.Tick(context.Background(), aged(stableView(dfwExtraInstance, dfwInstance, dfwExtraInstance, iadInstance), ages))
	if st := fresh.State(); st.IsLeader {
		t.Error("the Instance the scale-up created claimed the fleet")
	}
	if len(newcomer.writes) != 0 {
		t.Fatalf("a brand-new Instance scaled the fleet again: %+v", newcomer.writes)
	}
}

func TestFleetStable(t *testing.T) {
	cases := []struct {
		name string
		view View
		want bool
	}{
		{name: "every Instance running and every link up", view: stableView("a", "a", "b"), want: true},
		{name: "empty fleet", view: View{Self: "a"}, want: false},
		{
			name: "an Instance still starting",
			view: func() View {
				v := stableView("a", "a", "b")
				v.Instances[1].Status = StatusStarting
				return v
			}(),
		},
		{
			name: "an Instance stopping",
			view: func() View {
				v := stableView("a", "a", "b")
				v.Instances[1].Status = StatusStopping
				return v
			}(),
		},
		{
			name: "a degraded link",
			view: func() View {
				v := stableView("a", "a", "b")
				v.Edges[0].State = EdgeDegraded
				return v
			}(),
		},
		{
			name: "a link not yet measured",
			view: func() View {
				v := stableView("a", "a", "b")
				v.Edges = v.Edges[:1]
				return v
			}(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := fleetStable(c.view)
			if got != c.want {
				t.Errorf("fleetStable() = %v (%s), want %v", got, why, c.want)
			}
			if !got && why == "" {
				t.Error("an unstable fleet needs a reason to log")
			}
		})
	}
}

func TestCitiesFromPlacements(t *testing.T) {
	w := twoCityWorkload()
	w.Placements = append(w.Placements, placement("dfw-2", "DFW"), placement("dfw-3", "DFW"))
	w.PlacementStatus[1].Available = false

	got := citiesOf(&w)
	if len(got) != 2 {
		t.Fatalf("got %d cities: %+v", len(got), got)
	}
	if got[0].name != "dfw" || len(got[0].extras) != 2 || got[0].instances() != 3 || !got[0].ready {
		t.Errorf("dfw = %+v", got[0])
	}
	if got[0].extras[0] != "dfw-2" || got[0].extras[1] != "dfw-3" {
		t.Errorf("extras out of order: %+v", got[0].extras)
	}
	if got[1].name != "iad" || got[1].ready {
		t.Errorf("iad = %+v", got[1])
	}
}

// A base placement whose own name ends in a number is not an extra of
// something else.
func TestCitiesIgnoreUnrelatedSuffixes(t *testing.T) {
	w := datum.Workload{Placements: []datum.Placement{placement("us-west-1", "SJC"), placement("us-west-1-2", "SJC")}}
	got := citiesOf(&w)
	if len(got) != 1 || got[0].name != "us-west-1" || len(got[0].extras) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestAddAndRemoveExtraPlacement(t *testing.T) {
	w := twoCityWorkload()
	next, added, err := addExtra(w.Placements, "dfw")
	if err != nil {
		t.Fatal(err)
	}
	if added != "dfw-2" {
		t.Errorf("added %q, want dfw-2", added)
	}
	if len(next) != 3 {
		t.Fatalf("got %d placements", len(next))
	}
	extra := next[2]
	if extra.Name != "dfw-2" {
		t.Errorf("extra placed out of order: %+v", next)
	}
	// The copy has to select the same location, or the city grows elsewhere.
	labels := extra.LocationSelector["matchLabels"].(map[string]any)
	if labels["topology.datum.net/city-code"] != "DFW" {
		t.Errorf("extra selector = %+v", extra.LocationSelector)
	}
	if extra.ScaleSettings["minReplicas"] != 1 {
		t.Errorf("extra scaleSettings = %+v", extra.ScaleSettings)
	}

	// The next one up fills the next free number.
	next, added, err = addExtra(next, "dfw")
	if err != nil || added != "dfw-3" {
		t.Fatalf("added %q, err %v", added, err)
	}

	// Scaling down takes the highest extra and never the base.
	next, removed, err := removeExtra(next, "dfw")
	if err != nil || removed != "dfw-3" {
		t.Fatalf("removed %q, err %v", removed, err)
	}
	next, removed, err = removeExtra(next, "dfw")
	if err != nil || removed != "dfw-2" {
		t.Fatalf("removed %q, err %v", removed, err)
	}
	if len(next) != 2 {
		t.Fatalf("base placements lost: %+v", next)
	}
	if _, _, err := removeExtra(next, "dfw"); err == nil {
		t.Error("removing the base placement must be refused")
	}
}

func TestPlanWalksAStaircase(t *testing.T) {
	cities := []city{{name: "dfw", ready: true}, {name: "iad", ready: true}}
	// One city at a time, up then down, so the fleet returns to baseline.
	want := []struct {
		city string
		up   bool
	}{
		{"dfw", true}, {"iad", true}, {"dfw", false}, {"iad", false},
		// The second cycle starts with the other city, so the demo does not
		// always grow in the same place.
		{"iad", true}, {"dfw", true}, {"iad", false}, {"dfw", false},
	}
	state := cities
	for i, w := range want {
		act, ok := plan(state, i, 3)
		if !ok {
			t.Fatalf("step %d: no action planned", i)
		}
		if act.city != w.city || act.up != w.up {
			t.Fatalf("step %d: %s up=%v, want %s up=%v", i, act.city, act.up, w.city, w.up)
		}
		// Apply the action so the next step sees the fleet it created.
		state = applyForTest(state, act)
	}
}

func TestPlanRespectsFloorAndCeiling(t *testing.T) {
	cases := []struct {
		name   string
		cities []city
		step   int
		max    int
		want   string
		wantUp bool
		wantOK bool
	}{
		{
			name:   "a city at the ceiling is skipped for the next one",
			cities: []city{{name: "dfw", ready: true, extras: []string{"dfw-2"}}, {name: "iad", ready: true}},
			step:   0, max: 2,
			want: "iad", wantUp: true, wantOK: true,
		},
		{
			name:   "a city at the floor cannot scale down",
			cities: []city{{name: "dfw", ready: true}, {name: "iad", ready: true, extras: []string{"iad-2"}}},
			step:   2, max: 3,
			want: "iad", wantUp: false, wantOK: true,
		},
		{
			name:   "nothing to do when every city is at the ceiling and none can come down",
			cities: []city{{name: "dfw", ready: true}},
			step:   0, max: 1,
		},
		{
			name:   "a city whose placement is not ready is never touched",
			cities: []city{{name: "dfw", ready: false}},
			step:   0, max: 3,
		},
		{
			name:   "no cities at all",
			cities: nil,
			step:   0, max: 3,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			act, ok := plan(c.cities, c.step, c.max)
			if ok != c.wantOK {
				t.Fatalf("plan ok = %v, want %v (act %+v)", ok, c.wantOK, act)
			}
			if ok && (act.city != c.want || act.up != c.wantUp) {
				t.Fatalf("plan = %s up=%v, want %s up=%v", act.city, act.up, c.want, c.wantUp)
			}
		})
	}
}

// newTestDriver returns a driver on a frozen clock with a two-city workload.
func newTestDriver(t *testing.T, api *fakeWorkloads, now *time.Time) *Driver {
	t.Helper()
	return &Driver{
		API:           api,
		Workload:      "mesh",
		MaxPerCity:    3,
		Interval:      4 * time.Minute,
		SettleTimeout: 6 * time.Minute,
		Now:           func() time.Time { return *now },
	}
}

// takeTheFleet runs the tick on which this replica becomes the leader and then
// moves the clock past the interval, since the driver spaces its first change
// from the handover rather than acting the moment it takes over.
func takeTheFleet(t *testing.T, d *Driver, now *time.Time, v View) {
	t.Helper()
	d.Tick(context.Background(), v)
	if !d.State().IsLeader {
		t.Fatalf("the replica did not take the fleet")
	}
	*now = now.Add(d.Interval)
}

func TestDriverScalesUpWhenLeading(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload()}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	d := newTestDriver(t, api, &now)

	v := stableView("mesh-a", "mesh-a", "mesh-b")
	takeTheFleet(t, d, &now, v)
	d.Tick(context.Background(), v)
	if len(api.writes) != 1 {
		t.Fatalf("got %d writes, want 1", len(api.writes))
	}
	if len(api.writes[0]) != 3 || api.writes[0][2].Name != "dfw-2" {
		t.Fatalf("write = %+v", api.writes[0])
	}
	st := d.State()
	if !st.IsLeader || st.LastAction == "" || st.LastActionAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}

	// The fleet has not grown yet, so nothing else happens however long we
	// wait: one change at a time.
	now = now.Add(5 * time.Minute)
	d.Tick(context.Background(), v)
	if len(api.writes) != 1 {
		t.Fatalf("acted again while the last change was still settling: %d writes", len(api.writes))
	}
}

func TestDriverDoesNothingWhenNotLeading(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload()}
	now := time.Now()
	d := newTestDriver(t, api, &now)
	d.Tick(context.Background(), stableView("mesh-b", "mesh-a", "mesh-b"))
	if len(api.writes) != 0 {
		t.Fatalf("a follower wrote: %+v", api.writes)
	}
	if st := d.State(); st.IsLeader {
		t.Error("state claims leadership")
	}
}

func TestDriverWaitsForAStableFleet(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload()}
	now := time.Now()
	d := newTestDriver(t, api, &now)

	v := stableView("mesh-a", "mesh-a", "mesh-b")
	v.Instances[1].Status = StatusStarting
	d.Tick(context.Background(), v)
	if len(api.writes) != 0 {
		t.Fatalf("wrote while the fleet was unsettled: %+v", api.writes)
	}

	v = stableView("mesh-a", "mesh-a", "mesh-b")
	v.Edges[0].State = EdgeDown
	d.Tick(context.Background(), v)
	if len(api.writes) != 0 {
		t.Fatalf("wrote with a broken link: %+v", api.writes)
	}
}

func TestDriverHoldsItsInterval(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload()}
	now := time.Now()
	d := newTestDriver(t, api, &now)

	takeTheFleet(t, d, &now, stableView("mesh-a", "mesh-a", "mesh-b"))
	d.Tick(context.Background(), stableView("mesh-a", "mesh-a", "mesh-b"))
	if len(api.writes) != 1 {
		t.Fatalf("got %d writes", len(api.writes))
	}
	// The Instance the scale-up asked for has arrived, so the change settled.
	now = now.Add(30 * time.Second)
	grown := stableView("mesh-a", "mesh-a", "mesh-b", "mesh-c")
	d.Tick(context.Background(), grown)
	if len(api.writes) != 1 {
		t.Fatalf("acted inside the interval: %d writes", len(api.writes))
	}

	now = now.Add(4 * time.Minute)
	d.Tick(context.Background(), grown)
	if len(api.writes) != 2 {
		t.Fatalf("got %d writes after the interval, want 2", len(api.writes))
	}
	// Second step of the staircase: the other city grows.
	last := api.writes[1]
	if last[len(last)-1].Name != "iad-2" {
		t.Fatalf("second action = %+v", last)
	}
}

// The bug this guards: a driver with no change of its own to space from treated
// the interval as already elapsed, so a rollout's new leader could scale the
// fleet about a minute after every Instance had restarted. Observed in staging
// as a roll at 16:56:46 followed by a scale-up at 16:58:01.
func TestDriverWaitsAnIntervalAfterTakingTheFleet(t *testing.T) {
	start := time.Date(2026, 10, 2, 16, 56, 46, 0, time.UTC)
	const interval = 4 * time.Minute

	// Dallas is the older Instance, so it drives until it leaves the fleet.
	ages := map[string]time.Time{
		dfwInstance: start.Add(-24 * time.Hour),
		iadInstance: start.Add(-time.Hour),
	}
	// tick is one discovery tick: when it happens, measured from process start,
	// what the replica sees, and how many writes it should have made by then.
	type tick struct {
		at         time.Duration
		view       View
		wantWrites int
	}
	// Each case's views name the replica they belong to, so a tick can hand the
	// fleet from one replica to another.
	cases := []struct {
		name  string
		ticks []tick
	}{
		{
			name: "a fresh leader holds a stable fleet still for a full interval",
			ticks: []tick{
				{at: 0, view: aged(stableView(dfwInstance, dfwInstance, iadInstance), ages)},
				{at: 75 * time.Second, view: aged(stableView(dfwInstance, dfwInstance, iadInstance), ages)},
				{at: interval - time.Second, view: aged(stableView(dfwInstance, dfwInstance, iadInstance), ages)},
				{at: interval, view: aged(stableView(dfwInstance, dfwInstance, iadInstance), ages), wantWrites: 1},
			},
		},
		{
			name: "a replica that takes over later waits a full interval from the handover",
			ticks: []tick{
				// Dallas is driving, so this replica only watches, however long
				// its own process has been up.
				{at: 0, view: aged(stableView(iadInstance, dfwInstance, iadInstance), ages)},
				{at: 10 * time.Minute, view: aged(stableView(iadInstance, dfwInstance, iadInstance), ages)},
				// Dallas is gone: the handover, not process start, is what the
				// interval runs from.
				{at: 11 * time.Minute, view: aged(stableView(iadInstance, iadInstance), ages)},
				{at: 11*time.Minute + interval - time.Second, view: aged(stableView(iadInstance, iadInstance), ages)},
				{at: 11*time.Minute + interval, view: aged(stableView(iadInstance, iadInstance), ages), wantWrites: 1},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeWorkloads{workload: twoCityWorkload()}
			now := start
			d := newTestDriver(t, api, &now)
			d.Interval = interval
			for _, tk := range c.ticks {
				now = start.Add(tk.at)
				d.Tick(context.Background(), tk.view)
				if len(api.writes) != tk.wantWrites {
					t.Fatalf("%s after start: %d writes, want %d", tk.at, len(api.writes), tk.wantWrites)
				}
			}
		})
	}
}

func TestDriverRollsBackWhenAScaleUpDoesNotSettle(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload()}
	now := time.Now()
	d := newTestDriver(t, api, &now)

	v := stableView("mesh-a", "mesh-a", "mesh-b")
	takeTheFleet(t, d, &now, v)
	d.Tick(context.Background(), v)
	if len(api.writes) != 1 {
		t.Fatalf("got %d writes", len(api.writes))
	}

	// The Instance never arrives.
	now = now.Add(7 * time.Minute)
	d.Tick(context.Background(), v)
	if len(api.writes) != 2 {
		t.Fatalf("got %d writes, want the placement removed", len(api.writes))
	}
	if len(api.writes[1]) != 2 {
		t.Fatalf("rollback left %+v", api.writes[1])
	}
	st := d.State()
	if st.BackoffUntil.Before(now.Add(11 * time.Minute)) {
		t.Errorf("backoffUntil = %s, want three intervals out from %s", st.BackoffUntil, now)
	}

	// Nothing happens while backing off, even with a stable fleet.
	now = now.Add(5 * time.Minute)
	d.Tick(context.Background(), v)
	if len(api.writes) != 2 {
		t.Fatalf("acted during backoff: %d writes", len(api.writes))
	}
	now = now.Add(8 * time.Minute)
	d.Tick(context.Background(), v)
	if len(api.writes) != 3 {
		t.Fatalf("stayed backed off: %d writes", len(api.writes))
	}
}

func TestDriverTreatsAConflictAsALostRace(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload(), conflictOnce: true}
	now := time.Now()
	d := newTestDriver(t, api, &now)

	v := stableView("mesh-a", "mesh-a", "mesh-b")
	takeTheFleet(t, d, &now, v)
	tookOver := d.State().LastActionAt

	d.Tick(context.Background(), v)
	if len(api.writes) != 0 {
		t.Fatalf("a lost race was recorded as a write: %+v", api.writes)
	}
	if st := d.State(); !st.LastActionAt.Equal(tookOver) {
		t.Errorf("a lost race restarted the interval: %s, want %s", st.LastActionAt, tookOver)
	}
	// The next tick simply tries again, without waiting out another interval.
	d.Tick(context.Background(), v)
	if len(api.writes) != 1 {
		t.Fatalf("did not retry after the conflict: %d writes", len(api.writes))
	}
}

func TestDriverSurvivesAnUnreadableWorkload(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload(), getErr: errors.New("boom")}
	now := time.Now()
	d := newTestDriver(t, api, &now)
	takeTheFleet(t, d, &now, stableView("mesh-a", "mesh-a", "mesh-b"))
	d.Tick(context.Background(), stableView("mesh-a", "mesh-a", "mesh-b"))
	if len(api.writes) != 0 {
		t.Fatalf("wrote without reading: %+v", api.writes)
	}
}

func TestDriverStateReportsCities(t *testing.T) {
	w := twoCityWorkload()
	w.Placements = append(w.Placements, placement("dfw-2", "DFW"))
	api := &fakeWorkloads{workload: w}
	now := time.Now()
	d := newTestDriver(t, api, &now)
	// Dallas is already at the ceiling, so the staircase moves Ashburn.
	d.MaxPerCity = 2
	takeTheFleet(t, d, &now, stableView("mesh-a", "mesh-a", "mesh-b"))
	d.Tick(context.Background(), stableView("mesh-a", "mesh-a", "mesh-b"))

	st := d.State()
	if !st.Enabled {
		t.Error("a configured driver reports itself enabled")
	}
	if len(st.Cities) != 2 {
		t.Fatalf("cities = %+v", st.Cities)
	}
	if st.Cities[0].Name != "dfw" || st.Cities[0].Instances != 2 || st.Cities[0].Extras != 1 {
		t.Errorf("dfw = %+v", st.Cities[0])
	}
	if st.Cities[1].Name != "iad" || st.Cities[1].Instances != 2 || st.Cities[1].Extras != 1 {
		t.Errorf("iad = %+v", st.Cities[1])
	}
}

// applyForTest folds a planned action into the city list, so a test can walk
// several steps of the staircase.
func applyForTest(cities []city, act action) []city {
	out := append([]city(nil), cities...)
	for i := range out {
		if out[i].name != act.city {
			continue
		}
		if act.up {
			out[i].extras = append(append([]string(nil), out[i].extras...), extraName(act.city, len(out[i].extras)+2))
		} else {
			out[i].extras = out[i].extras[:len(out[i].extras)-1]
		}
	}
	return out
}

// driverSource serves a view carrying driver state, the way Live does.
type driverSource struct{ state *DriverState }

func (s driverSource) View(context.Context) View { return View{Self: "a", Driver: s.state} }
func (driverSource) Local() LocalReport          { return LocalReport{Name: "a"} }

// The page ignores the driver, so its only contract is that the key is there
// when the driver is on and absent when it is off.
func TestFleetViewCarriesDriverStateOnlyWhenEnabled(t *testing.T) {
	for _, c := range []struct {
		name  string
		state *DriverState
		want  bool
	}{
		{name: "off", state: nil},
		{name: "on", state: &DriverState{Enabled: true, IsLeader: true}, want: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(NewHandler(driverSource{state: c.state}, fstest.MapFS{}))
			defer srv.Close()
			resp, err := srv.Client().Get(srv.URL + "/api/mesh")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			got, ok := body["driver"]
			if ok != c.want {
				t.Fatalf("driver key present = %v, want %v (body %+v)", ok, c.want, body)
			}
			if ok && got.(map[string]any)["isLeader"] != true {
				t.Errorf("driver = %+v", got)
			}
		})
	}
}
