package mesh

import (
	"sort"
	"sync"
	"time"
)

// Activity event types. The page turns these into sentences; the JSON carries
// facts rather than prose so the wording stays in one place.
const (
	ActivityScaledUp   = "scaled-up"
	ActivityScaledDown = "scaled-down"
	ActivityStarting   = "instance-starting"
	ActivityReady      = "instance-ready"
	ActivityStopping   = "instance-stopping"
)

// MaxActivity caps what the fleet view carries. Scaling is rare next to
// traffic, so this is a couple of minutes of history — enough to fill the
// longest list a tall window asks for and still have some behind it.
const MaxActivity = 32

// Activity is one thing the platform did on its own: a workload scaled, or an
// Instance started, joined the mesh or drained.
type Activity struct {
	At       time.Time `json:"at"`
	Type     string    `json:"type"`
	Location string    `json:"location"`
	City     string    `json:"city,omitempty"`
	Instance string    `json:"instance,omitempty"`
	// From and To are the Instance counts either side of a scaling decision.
	From int `json:"from,omitempty"`
	To   int `json:"to,omitempty"`
	// JoinMs is how long the Instance took to become reachable by every peer
	// over the private network, carried on a ready event. It is the headline
	// of the whole demo, so it travels with the event rather than being
	// worked out again on the page.
	JoinMs float64 `json:"joinMs,omitempty"`
}

// activityLog narrates a fleet by comparing what it looks like now with what
// it looked like a moment ago. Discovery already reports each Instance's
// state, so the same code serves a simulated fleet and a real one.
type activityLog struct {
	mu     sync.Mutex
	events []Activity
	seen   map[string]string
	counts map[string]int
	primed bool
}

func newActivityLog() *activityLog {
	return &activityLog{seen: map[string]string{}, counts: map[string]int{}}
}

// observe records what changed since the last look. The first observation only
// establishes the baseline: a fleet that was already running did not just
// start.
func (a *activityLog) observe(instances []InstanceView, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	status := make(map[string]string, len(instances))
	counts := make(map[string]int, len(instances))
	city := map[string]string{}
	for _, inst := range instances {
		status[inst.Name] = inst.Status
		city[inst.Location] = inst.City
		// An Instance on its way out no longer counts towards the workload's
		// size; the scale-down is the news, not its last few seconds.
		if inst.Status != StatusStopping {
			counts[inst.Location]++
		}
	}

	if !a.primed {
		a.seen, a.counts, a.primed = status, counts, true
		return
	}

	// Instance events first, so the scaling decision that caused them ends up
	// above them in a newest-first list.
	var batch []Activity
	names := make([]string, 0, len(status))
	for name := range status {
		names = append(names, name)
	}
	sort.Strings(names)
	byName := make(map[string]InstanceView, len(instances))
	for _, inst := range instances {
		byName[inst.Name] = inst
	}
	for _, name := range names {
		inst := byName[name]
		was, known := a.seen[name]
		switch {
		case !known && inst.Status == StatusStarting:
			batch = append(batch, Activity{Type: ActivityStarting, Instance: name, Location: inst.Location, City: inst.City})
		// An Instance that was already up by the time discovery next looked
		// still joined the mesh; it just did it between two observations.
		case was != StatusRunning && inst.Status == StatusRunning:
			batch = append(batch, Activity{Type: ActivityReady, Instance: name, Location: inst.Location, City: inst.City, JoinMs: inst.JoinMs})
		case known && was != StatusStopping && inst.Status == StatusStopping:
			batch = append(batch, Activity{Type: ActivityStopping, Instance: name, Location: inst.Location, City: inst.City})
		}
	}

	locations := make([]string, 0, len(counts))
	for location := range counts {
		locations = append(locations, location)
	}
	for location := range a.counts {
		if _, ok := counts[location]; !ok {
			locations = append(locations, location)
		}
	}
	sort.Strings(locations)
	for _, location := range locations {
		was, to := a.counts[location], counts[location]
		if was == to {
			continue
		}
		kind := ActivityScaledUp
		if to < was {
			kind = ActivityScaledDown
		}
		batch = append(batch, Activity{Type: kind, Location: location, City: city[location], From: was, To: to})
	}

	for _, e := range batch {
		e.At = now
		a.events = append([]Activity{e}, a.events...)
	}
	if len(a.events) > MaxActivity {
		a.events = a.events[:MaxActivity]
	}
	a.seen, a.counts = status, counts
}

// recent returns the log newest first.
func (a *activityLog) recent() []Activity {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Activity{}, a.events...)
}
