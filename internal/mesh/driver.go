package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/datum"
)

// WorkloadWriter is the Datum Cloud API as the fleet driver uses it.
type WorkloadWriter interface {
	GetWorkload(ctx context.Context, name string) (*datum.Workload, error)
	PatchWorkloadPlacements(ctx context.Context, name string, placements []datum.Placement, resourceVersion string) error
}

// Driver makes the demo workload scale itself, so the page's narration has
// real scaling decisions to tell rather than a fleet that never changes.
//
// It drives the workload by adding and removing placements rather than by
// moving an existing placement's minReplicas: a change to minReplicas on a
// placement that already exists is not currently acted on, while a new
// placement is picked up within seconds.
//
// Every replica runs this code, so one of them has to be chosen. The choice is
// made from what discovery already reports — the lowest-named running Instance
// drives — and is re-made on every tick. Two replicas briefly agreeing they
// both lead is harmless: an action writes a desired shape, and writing the
// same shape twice is the same as writing it once.
type Driver struct {
	API      WorkloadWriter
	Workload string
	// MaxPerCity caps a city's Instances. The floor is always one, since a
	// city that empties stops being part of the mesh the page is about.
	MaxPerCity int
	// Interval is the shortest gap between two changes. The demo is watched,
	// not benchmarked, so it moves slowly enough to read.
	Interval time.Duration
	// SettleTimeout is how long a scale-up has to produce a reachable
	// Instance before the driver takes the placement back out.
	SettleTimeout time.Duration
	// Now is the clock, so tests can walk hours of cadence in an instant.
	Now func() time.Time

	// ticking serialises ticks without queueing them: a tick that arrives
	// while an API call is in flight is dropped, not stacked behind it.
	ticking sync.Mutex

	mu           sync.Mutex
	leader       bool
	step         int
	lastAction   string
	lastActionAt time.Time
	backoffUntil time.Time
	cities       []city
	pending      *pendingAction
}

// pendingAction is a scale-up waiting for its Instance to join the mesh.
type pendingAction struct {
	city      string
	placement string
	at        time.Time
	// wantInstances is the fleet size that proves the new Instance arrived.
	wantInstances int
}

// DriverState is what the fleet view reports about the driver, so an operator
// can see which replica is driving and what it last did without reading logs.
type DriverState struct {
	Enabled      bool           `json:"enabled"`
	IsLeader     bool           `json:"isLeader"`
	LastAction   string         `json:"lastAction,omitempty"`
	LastActionAt time.Time      `json:"lastActionAt,omitzero"`
	BackoffUntil time.Time      `json:"backoffUntil,omitzero"`
	Cities       []DriverCity   `json:"cities,omitempty"`
	Pending      *DriverPending `json:"pending,omitempty"`
}

// DriverCity is one city's size as the driver last read it.
type DriverCity struct {
	Name string `json:"name"`
	// Instances is the city's desired size: its base placement plus its extras.
	Instances int  `json:"instances"`
	Extras    int  `json:"extras"`
	Ready     bool `json:"ready"`
}

// DriverPending is a change the driver is still waiting on.
type DriverPending struct {
	City      string    `json:"city"`
	Placement string    `json:"placement"`
	Since     time.Time `json:"since"`
}

// city is one base placement and the extras stacked on it.
type city struct {
	name   string
	extras []string
	ready  bool
}

// instances is the city's desired size, which is the base placement's single
// Instance plus one per extra.
func (c city) instances() int { return 1 + len(c.extras) }

// Tick re-runs the election and, if this replica leads, makes at most one
// change. It is cheap and silent whenever there is nothing to do.
func (d *Driver) Tick(ctx context.Context, v View) {
	if !d.ticking.TryLock() {
		return
	}
	defer d.ticking.Unlock()

	now := d.now()
	lead := leads(v)
	d.mu.Lock()
	was := d.leader
	d.leader = lead
	pending := d.pending
	backoff := d.backoffUntil
	lastAt := d.lastActionAt
	d.mu.Unlock()
	if lead != was {
		if lead {
			slog.Info("driving the fleet", "instance", v.Self, "workload", d.Workload)
		} else {
			slog.Info("handing the fleet over", "instance", v.Self, "workload", d.Workload)
		}
	}
	if !lead {
		return
	}

	stable, why := fleetStable(v)
	if pending != nil {
		d.settle(ctx, v, pending, stable, why, now)
		return
	}
	if now.Before(backoff) {
		return
	}
	if !lastAt.IsZero() && now.Sub(lastAt) < d.Interval {
		return
	}
	if !stable {
		slog.Debug("fleet not settled, leaving the workload alone", "reason", why)
		return
	}
	d.act(ctx, v, now)
}

// settle decides what to do about a scale-up that has not finished yet: wait,
// accept it, or take it back out.
func (d *Driver) settle(ctx context.Context, v View, p *pendingAction, stable bool, why string, now time.Time) {
	if stable && len(v.Instances) >= p.wantInstances {
		slog.Info("scale-up settled", "city", p.city, "placement", p.placement,
			"instances", len(v.Instances), "after", now.Sub(p.at).Round(time.Second))
		d.mu.Lock()
		d.pending = nil
		d.mu.Unlock()
		return
	}
	if now.Sub(p.at) < d.SettleTimeout {
		slog.Debug("waiting for a scale-up to settle", "city", p.city, "placement", p.placement, "reason", why)
		return
	}

	slog.Warn("scale-up did not settle, removing the placement", "city", p.city, "placement", p.placement,
		"timeout", d.SettleTimeout, "reason", why)
	w, err := d.API.GetWorkload(ctx, d.Workload)
	if err != nil {
		slog.Error("cannot read the workload to roll back a scale-up", "error", err)
		return
	}
	after := w.Placements
	next, removed, err := removePlacement(w.Placements, p.placement)
	if err != nil {
		// Someone else already took it out; the rollback is done.
		slog.Info("placement already gone, nothing to roll back", "placement", p.placement)
	} else if err := d.API.PatchWorkloadPlacements(ctx, d.Workload, next, w.ResourceVersion); err != nil {
		if errors.Is(err, datum.ErrConflict) {
			slog.Info("another replica wrote first, retrying the rollback on the next tick", "placement", p.placement)
			return
		}
		slog.Error("cannot remove the placement that did not settle", "placement", removed, "error", err)
		return
	} else {
		after = next
	}

	d.mu.Lock()
	d.pending = nil
	d.cities = citiesOf(&datum.Workload{Placements: after, PlacementStatus: w.PlacementStatus})
	d.step++
	d.lastAction = fmt.Sprintf("rolled back %s in %s after it did not settle", p.placement, p.city)
	d.lastActionAt = now
	// Back off well past a single interval: a city that cannot start an
	// Instance will not be ready again in four minutes.
	d.backoffUntil = now.Add(3 * d.Interval)
	d.mu.Unlock()
}

// act makes one change: the next step of the staircase, if the workload allows
// it.
func (d *Driver) act(ctx context.Context, v View, now time.Time) {
	w, err := d.API.GetWorkload(ctx, d.Workload)
	if err != nil {
		slog.Error("cannot read the workload to scale it", "workload", d.Workload, "error", err)
		return
	}
	cities := citiesOf(w)
	d.mu.Lock()
	d.cities = cities
	step := d.step
	d.mu.Unlock()

	act, ok := plan(cities, step, d.max())
	if !ok {
		slog.Debug("no city can move", "cities", len(cities))
		return
	}

	var next []datum.Placement
	var changed string
	if act.up {
		next, changed, err = addExtra(w.Placements, act.city)
	} else {
		next, changed, err = removeExtra(w.Placements, act.city)
	}
	if err != nil {
		slog.Error("cannot work out the new placement set", "city", act.city, "error", err)
		return
	}

	direction := "down"
	if act.up {
		direction = "up"
	}
	if err := d.API.PatchWorkloadPlacements(ctx, d.Workload, next, w.ResourceVersion); err != nil {
		if errors.Is(err, datum.ErrConflict) {
			slog.Info("another replica scaled the workload first, waiting for the next tick",
				"city", act.city, "action", direction)
			return
		}
		slog.Error("cannot scale the workload", "city", act.city, "action", direction, "placement", changed, "error", err)
		return
	}
	slog.Info("scaled a city", "city", act.city, "action", direction, "placement", changed,
		"instances", len(v.Instances), "reason", "the staircase's next step on a settled fleet")

	d.mu.Lock()
	defer d.mu.Unlock()
	d.step = step + 1
	d.cities = citiesOf(&datum.Workload{Placements: next, PlacementStatus: w.PlacementStatus})
	d.lastAction = fmt.Sprintf("scaled %s %s by %s", act.city, direction, changed)
	d.lastActionAt = now
	if act.up {
		// A scale-up is only finished once the Instance it asked for is in the
		// mesh, so nothing else moves until it is.
		d.pending = &pendingAction{city: act.city, placement: changed, at: now, wantInstances: len(v.Instances) + 1}
	}
}

// State returns what the fleet view reports about the driver.
func (d *Driver) State() DriverState {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := DriverState{
		Enabled:      true,
		IsLeader:     d.leader,
		LastAction:   d.lastAction,
		LastActionAt: d.lastActionAt,
		BackoffUntil: d.backoffUntil,
	}
	for _, c := range d.cities {
		st.Cities = append(st.Cities, DriverCity{Name: c.name, Instances: c.instances(), Extras: len(c.extras), Ready: c.ready})
	}
	if d.pending != nil {
		st.Pending = &DriverPending{City: d.pending.city, Placement: d.pending.placement, Since: d.pending.at}
	}
	return st
}

func (d *Driver) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Driver) max() int {
	if d.MaxPerCity < 1 {
		return 1
	}
	return d.MaxPerCity
}

// leads reports whether this replica is the one that drives the workload: the
// lowest-named Instance that discovery currently sees running.
func leads(v View) bool {
	if v.Self == "" {
		return false
	}
	lowest := ""
	for _, inst := range v.Instances {
		if inst.Status != StatusRunning {
			continue
		}
		if lowest == "" || inst.Name < lowest {
			lowest = inst.Name
		}
	}
	return lowest != "" && lowest == v.Self
}

// fleetStable reports whether the fleet is quiet enough to change: every
// Instance running, none draining, and every Instance reaching every other
// one. When it is not, the reason is returned for the log.
func fleetStable(v View) (bool, string) {
	if len(v.Instances) == 0 {
		return false, "no instances"
	}
	for _, inst := range v.Instances {
		if inst.Status != StatusRunning {
			return false, fmt.Sprintf("%s is %s", inst.Name, inst.Status)
		}
	}
	want := len(v.Instances) * (len(v.Instances) - 1)
	if len(v.Edges) != want {
		return false, fmt.Sprintf("%d of %d links measured", len(v.Edges), want)
	}
	for _, e := range v.Edges {
		if e.State != EdgeUp {
			return false, fmt.Sprintf("link %s to %s is %s", e.From, e.To, e.State)
		}
	}
	return true, ""
}

// citiesOf groups a workload's placements into cities: a base placement and
// the extras named after it, sorted by name so every replica plans the same
// staircase.
func citiesOf(w *datum.Workload) []city {
	names := make(map[string]bool, len(w.Placements))
	for _, p := range w.Placements {
		names[p.Name] = true
	}
	extras := map[string][]string{}
	var bases []string
	for _, p := range w.Placements {
		if base, _, ok := splitExtra(p.Name); ok && names[base] {
			extras[base] = append(extras[base], p.Name)
			continue
		}
		bases = append(bases, p.Name)
	}
	sort.Strings(bases)
	out := make([]city, 0, len(bases))
	for _, base := range bases {
		list := extras[base]
		sort.Slice(list, func(i, j int) bool { return extraNumber(list[i]) < extraNumber(list[j]) })
		out = append(out, city{name: base, extras: list, ready: w.PlacementReady(base)})
	}
	return out
}

// action is one step of the staircase.
type action struct {
	city string
	up   bool
}

// plan returns the step the staircase is on, skipping steps a city cannot
// take. The sequence walks every city up one at a time and then back down, so
// no two cities ever move together and the fleet returns to its baseline
// between cycles. Each cycle starts with a different city, so the demo does
// not always grow in the same place.
func plan(cities []city, step, maxPerCity int) (action, bool) {
	movable := make([]city, 0, len(cities))
	for _, c := range cities {
		if c.ready {
			movable = append(movable, c)
		}
	}
	n := len(movable)
	if n == 0 {
		return action{}, false
	}
	for attempt := 0; attempt < 2*n; attempt++ {
		s := step + attempt
		cycle, pos := s/(2*n), s%(2*n)
		up := pos < n
		c := movable[(pos%n+cycle%n)%n]
		if up && c.instances() < maxPerCity {
			return action{city: c.name, up: true}, true
		}
		if !up && len(c.extras) > 0 {
			return action{city: c.name, up: false}, true
		}
	}
	return action{}, false
}

// addExtra returns the placement set with one more Instance in the named city.
// The extra copies the base placement's location selector, so the city grows
// where it already is, and asks for a single Instance.
func addExtra(placements []datum.Placement, base string) ([]datum.Placement, string, error) {
	var selector map[string]any
	found := false
	highest := 1
	for _, p := range placements {
		if p.Name == base {
			selector, found = p.LocationSelector, true
		}
		if b, n, ok := splitExtra(p.Name); ok && b == base {
			highest = max(highest, n)
		}
	}
	if !found {
		return nil, "", fmt.Errorf("placement %q not found", base)
	}
	copied, err := deepCopy(selector)
	if err != nil {
		return nil, "", fmt.Errorf("copy the location selector of %q: %w", base, err)
	}
	name := extraName(base, highest+1)
	next := append(append([]datum.Placement(nil), placements...), datum.Placement{
		Name:             name,
		LocationSelector: copied,
		ScaleSettings:    map[string]any{"minReplicas": 1},
	})
	return next, name, nil
}

// removeExtra returns the placement set with the city's highest-numbered extra
// taken out. A base placement is never removed: a city that empties stops
// being part of the mesh.
func removeExtra(placements []datum.Placement, base string) ([]datum.Placement, string, error) {
	name, number := "", 0
	for _, p := range placements {
		if b, n, ok := splitExtra(p.Name); ok && b == base && n > number {
			name, number = p.Name, n
		}
	}
	if name == "" {
		return nil, "", fmt.Errorf("city %q has no extra placement to remove", base)
	}
	next, _, err := removePlacement(placements, name)
	return next, name, err
}

// removePlacement drops one placement by name.
func removePlacement(placements []datum.Placement, name string) ([]datum.Placement, string, error) {
	out := make([]datum.Placement, 0, len(placements))
	found := false
	for _, p := range placements {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return nil, "", fmt.Errorf("placement %q not found", name)
	}
	return out, name, nil
}

// extraName is how an extra placement is named: the base placement's name and
// the Instance's number within the city, counting the base as one.
func extraName(base string, n int) string { return base + "-" + strconv.Itoa(n) }

// splitExtra reads an extra placement's name back into the base it belongs to
// and its number. Numbers start at two, so a name ending in -1 or -0 is a
// placement in its own right rather than an extra.
func splitExtra(name string) (string, int, bool) {
	base, suffix, ok := cutLast(name, "-")
	if !ok || base == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || n < 2 {
		return "", 0, false
	}
	return base, n, true
}

func extraNumber(name string) int {
	_, n, _ := splitExtra(name)
	return n
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// deepCopy copies a parsed JSON object, so an added placement never shares a
// map with the placement it was copied from.
func deepCopy(in map[string]any) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
