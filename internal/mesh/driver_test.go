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

func TestLowestRunningLeadsTheFleet(t *testing.T) {
	cases := []struct {
		name string
		view View
		want bool
	}{
		{
			name: "lowest name leads",
			view: stableView("mesh-a-0", "mesh-a-0", "mesh-b-0"),
			want: true,
		},
		{
			name: "a lower peer leads instead",
			view: stableView("mesh-b-0", "mesh-a-0", "mesh-b-0"),
			want: false,
		},
		{
			name: "sole Instance leads",
			view: stableView("mesh-a-0", "mesh-a-0"),
			want: true,
		},
		{
			name: "an Instance that has not identified itself never leads",
			view: stableView("", "mesh-a-0"),
			want: false,
		},
		{
			name: "a lower name that is still starting does not lead",
			view: func() View {
				v := stableView("mesh-b-0", "mesh-b-0")
				v.Instances = append([]InstanceView{{Name: "mesh-a-0", Status: StatusStarting}}, v.Instances...)
				return v
			}(),
			want: true,
		},
		{
			name: "a lower name that is stopping does not lead",
			view: func() View {
				v := stableView("mesh-b-0", "mesh-b-0")
				v.Instances = append([]InstanceView{{Name: "mesh-a-0", Status: StatusStopping}}, v.Instances...)
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

func TestDriverScalesUpWhenLeading(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload()}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	d := newTestDriver(t, api, &now)

	d.Tick(context.Background(), stableView("mesh-a", "mesh-a", "mesh-b"))
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
	d.Tick(context.Background(), stableView("mesh-a", "mesh-a", "mesh-b"))
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

func TestDriverRollsBackWhenAScaleUpDoesNotSettle(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload()}
	now := time.Now()
	d := newTestDriver(t, api, &now)

	v := stableView("mesh-a", "mesh-a", "mesh-b")
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
	d.Tick(context.Background(), v)
	if len(api.writes) != 0 {
		t.Fatalf("a lost race was recorded as a write: %+v", api.writes)
	}
	if st := d.State(); !st.LastActionAt.IsZero() {
		t.Error("a lost race must not start the interval")
	}
	// The next tick simply tries again.
	d.Tick(context.Background(), v)
	if len(api.writes) != 1 {
		t.Fatalf("did not retry after the conflict: %d writes", len(api.writes))
	}
}

func TestDriverSurvivesAnUnreadableWorkload(t *testing.T) {
	api := &fakeWorkloads{workload: twoCityWorkload(), getErr: errors.New("boom")}
	now := time.Now()
	d := newTestDriver(t, api, &now)
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
