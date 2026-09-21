// Package mesh measures traffic between a workload's Instances over their
// private network and assembles the fleet-wide view the page renders.
package mesh

import (
	"sort"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/datum"
	"github.com/datum-labs/compute-network-demo/internal/geo"
)

// Edge states, from the perspective of the instance sending traffic.
const (
	EdgeUp       = "up"
	EdgeDegraded = "degraded"
	EdgeDown     = "down"
	EdgePending  = "pending"
)

// Instance lifecycle states, as the page shows them.
const (
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusStopping = "stopping"
)

// PeerStats is what one instance measured towards one peer.
type PeerStats struct {
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	RTTMs       float64   `json:"rttMs"`
	SuccessRate float64   `json:"successRate"`
	Attempts    int64     `json:"attempts"`
	Messages    int64     `json:"messages"`
	Bytes       int64     `json:"bytes"`
	LastSuccess time.Time `json:"lastSuccess,omitzero"`
}

// Exchange is one message an instance sent to a peer and the reply it got.
// The feed on the page is built from these, so the JSON names are short: a
// page polls every couple of seconds and every instance contributes.
type Exchange struct {
	At    time.Time `json:"at"`
	To    string    `json:"to"`
	RTTMs float64   `json:"rttMs"`
	OK    bool      `json:"ok"`
	// From is filled in by the instance assembling the fleet view, since a
	// report's exchanges are all its own.
	From string `json:"from,omitempty"`
}

// LocalReport is served at /mesh/local: one instance's own measurements.
type LocalReport struct {
	Name      string      `json:"name"`
	Location  string      `json:"location"`
	StartedAt time.Time   `json:"startedAt"`
	Peers     []PeerStats `json:"peers"`
	Exchanges []Exchange  `json:"exchanges,omitempty"`
}

// MaxFeedExchanges caps what the fleet view carries. The page shows around
// twenty, and the payload is polled every two seconds.
const MaxFeedExchanges = 24

// InstanceView is an instance as the page shows it.
type InstanceView struct {
	Name           string  `json:"name"`
	Location       string  `json:"location"`
	CityCode       string  `json:"cityCode"`
	City           string  `json:"city"`
	Country        string  `json:"country"`
	CountryCode    string  `json:"countryCode"`
	Lat            float64 `json:"lat"`
	Lon            float64 `json:"lon"`
	PrivateIP      string  `json:"privateIP"`
	Status         string  `json:"status"`
	IsSelf         bool    `json:"isSelf"`
	UptimeSeconds  int64   `json:"uptimeSeconds"`
	PeersReachable int     `json:"peersReachable"`
	PeersTotal     int     `json:"peersTotal"`
	// JoinMs is how long this instance took to become reachable by every peer
	// over the private network once it was running.
	JoinMs float64 `json:"joinMs,omitempty"`
	// Reporting is false when the serving instance could not collect this
	// instance's own measurements.
	Reporting bool `json:"reporting"`
}

// EdgeView is directed traffic from one instance to another.
type EdgeView struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	RTTMs       float64 `json:"rttMs"`
	SuccessRate float64 `json:"successRate"`
	Messages    int64   `json:"messages"`
	Bytes       int64   `json:"bytes"`
	State       string  `json:"state"`
}

// Totals are the headline numbers.
type Totals struct {
	Regions             int     `json:"regions"`
	Instances           int     `json:"instances"`
	Messages            int64   `json:"messages"`
	Bytes               int64   `json:"bytes"`
	AvgRTTMs            float64 `json:"avgRttMs"`
	PublicInternetBytes int64   `json:"publicInternetBytes"`
}

// View is the response of /api/mesh.
type View struct {
	Mode        string         `json:"mode"`
	Discovery   string         `json:"discovery,omitempty"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Project     string         `json:"project,omitempty"`
	Workload    string         `json:"workload,omitempty"`
	Self        string         `json:"self"`
	Instances   []InstanceView `json:"instances"`
	Edges       []EdgeView     `json:"edges"`
	// Exchanges are the most recent messages across the whole fleet, newest
	// first, for the live feed.
	Exchanges []Exchange `json:"exchanges"`
	// Activity is what the platform did on its own, newest first: scaling
	// decisions and the instance lifecycles they set off.
	Activity []Activity `json:"activity"`
	Totals   Totals     `json:"totals"`
	// Notice is a plain-language explanation shown when the view is partial.
	Notice string `json:"notice,omitempty"`
}

// InstanceStatus is where an Instance is in its life, as the page shows it.
// Draining wins over available: an Instance on its way out still answers right
// up until it goes.
func InstanceStatus(inst datum.Instance) string {
	switch {
	case inst.Stopping:
		return StatusStopping
	case inst.Available:
		return StatusRunning
	default:
		return StatusStarting
	}
}

// EdgeState classifies a peer's recent reachability.
func EdgeState(s PeerStats) string {
	switch {
	case s.Attempts == 0:
		return EdgePending
	case s.SuccessRate >= 0.9:
		return EdgeUp
	case s.SuccessRate > 0:
		return EdgeDegraded
	default:
		return EdgeDown
	}
}

// Assemble builds the fleet view from discovered instances and whatever
// per-instance reports could be collected. Instances without an address or
// a report still appear; they simply contribute no outgoing edges.
func Assemble(instances []datum.Instance, dir *geo.Directory, self string, reports map[string]*LocalReport, now time.Time) View {
	v := View{GeneratedAt: now, Self: self, Instances: []InstanceView{}, Edges: []EdgeView{}, Exchanges: []Exchange{}, Activity: []Activity{}}

	known := make(map[string]bool, len(instances))
	for _, inst := range instances {
		known[inst.Name] = true
	}

	regions := map[string]bool{}
	for _, inst := range instances {
		place, _ := dir.Lookup(inst.Location)
		iv := InstanceView{
			Name:        inst.Name,
			Location:    inst.Location,
			CityCode:    place.CityCode,
			City:        place.City,
			Country:     place.Country,
			CountryCode: place.CountryCode,
			Lat:         place.Lat,
			Lon:         place.Lon,
			Status:      InstanceStatus(inst),
			IsSelf:      inst.Name == self,
		}
		if inst.PrivateIP.IsValid() {
			iv.PrivateIP = inst.PrivateIP.String()
		}
		// Discovery only sees when an instance became available, which covers
		// booting as well as joining the network. A fleet that measures the
		// join itself reports it outright.
		switch {
		case inst.JoinMs > 0:
			iv.JoinMs = inst.JoinMs
		case !inst.AvailableAt.IsZero() && !inst.CreatedAt.IsZero() && inst.AvailableAt.After(inst.CreatedAt):
			iv.JoinMs = float64(inst.AvailableAt.Sub(inst.CreatedAt).Milliseconds())
		}
		started := inst.AvailableAt
		if started.IsZero() || !inst.Available {
			started = inst.CreatedAt
		}
		if r := reports[inst.Name]; r != nil {
			iv.Reporting = true
			if !r.StartedAt.IsZero() {
				started = r.StartedAt
			}
		}
		if !started.IsZero() && now.After(started) {
			iv.UptimeSeconds = int64(now.Sub(started).Seconds())
		}
		v.Instances = append(v.Instances, iv)
		if inst.Location != "" {
			regions[inst.Location] = true
		}
	}

	var rttSum float64
	var rttCount int
	for _, inst := range instances {
		r := reports[inst.Name]
		if r == nil {
			continue
		}
		for _, p := range r.Peers {
			if !known[p.Name] || p.Name == inst.Name {
				continue
			}
			e := EdgeView{
				From:        inst.Name,
				To:          p.Name,
				RTTMs:       p.RTTMs,
				SuccessRate: p.SuccessRate,
				Messages:    p.Messages,
				Bytes:       p.Bytes,
				State:       EdgeState(p),
			}
			v.Edges = append(v.Edges, e)
			v.Totals.Messages += e.Messages
			v.Totals.Bytes += e.Bytes
			if e.RTTMs > 0 && (e.State == EdgeUp || e.State == EdgeDegraded) {
				rttSum += e.RTTMs
				rttCount++
			}
		}
	}
	// Newest first, capped: the feed only ever shows the last few seconds.
	for _, inst := range instances {
		r := reports[inst.Name]
		if r == nil {
			continue
		}
		for _, e := range r.Exchanges {
			if !known[e.To] || e.To == inst.Name {
				continue
			}
			e.From = inst.Name
			v.Exchanges = append(v.Exchanges, e)
		}
	}
	sort.Slice(v.Exchanges, func(i, j int) bool { return v.Exchanges[i].At.After(v.Exchanges[j].At) })
	if len(v.Exchanges) > MaxFeedExchanges {
		v.Exchanges = v.Exchanges[:MaxFeedExchanges]
	}

	sort.Slice(v.Edges, func(i, j int) bool {
		if v.Edges[i].From != v.Edges[j].From {
			return v.Edges[i].From < v.Edges[j].From
		}
		return v.Edges[i].To < v.Edges[j].To
	})

	// Reachability counts an instance's own view when it reported, and
	// otherwise what its peers observed when reaching it.
	for i := range v.Instances {
		iv := &v.Instances[i]
		iv.PeersTotal = len(instances) - 1
		for _, e := range v.Edges {
			reachable := e.State == EdgeUp || e.State == EdgeDegraded
			if iv.Reporting && e.From == iv.Name && reachable {
				iv.PeersReachable++
			}
			if !iv.Reporting && e.To == iv.Name && reachable {
				iv.PeersReachable++
			}
		}
	}

	v.Totals.Instances = len(instances)
	v.Totals.Regions = len(regions)
	if rttCount > 0 {
		v.Totals.AvgRTTMs = rttSum / float64(rttCount)
	}
	return v
}
