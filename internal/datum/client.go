// Package datum reads the Instances of a workload, and the Locations they run
// in, from the Datum Cloud API using plain HTTP and JSON.
package datum

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/geo"
)

const (
	labelWorkloadName = "compute.datumapis.com/workload-name"
	labelLocation     = "compute.datumapis.com/location"

	topologyCityCode    = "topology.datum.net/city-code"
	topologyCity        = "topology.datum.net/city"
	topologyCountry     = "topology.datum.net/country"
	topologyCountryCode = "topology.datum.net/country-code"
)

// TokenSource supplies bearer tokens for API calls.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Client talks to one project's control plane.
type Client struct {
	APIURL  string
	Project string
	Tokens  TokenSource
	HTTP    *http.Client
}

// Instance is the part of a compute Instance the mesh needs.
type Instance struct {
	Name      string
	Location  string
	PrivateIP netip.Addr
	// Prefix is the full address as reported, which may cover more than the
	// single address the interface holds.
	Prefix      netip.Prefix
	Available   bool
	CreatedAt   time.Time
	AvailableAt time.Time
	// Stopping is set once the Instance is being torn down, so the page can
	// show it draining rather than having it disappear mid-conversation, and
	// StoppingAt is when that started.
	Stopping     bool
	StoppingAt   time.Time
	StatusReason string
	// JoinMs is how long the Instance took to become reachable by every peer
	// over the private network. Discovery cannot see this; a fleet that
	// measures it for itself fills it in.
	JoinMs float64
}

func (c *Client) projectBase() string {
	return strings.TrimRight(c.APIURL, "/") + "/apis/resourcemanager.miloapis.com/v1alpha1/projects/" +
		url.PathEscape(c.Project) + "/control-plane"
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.projectBase()+path, nil)
	if err != nil {
		return err
	}
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, msg)
	}
	return json.Unmarshal(body, out)
}

// ListInstances returns the workload's Instances, sorted by name.
func (c *Client) ListInstances(ctx context.Context, workload string) ([]Instance, error) {
	q := url.Values{"labelSelector": {labelWorkloadName + "=" + workload}}
	var list instanceList
	if err := c.get(ctx, "/apis/compute.datumapis.com/v1alpha/namespaces/default/instances?"+q.Encode(), &list); err != nil {
		return nil, err
	}
	return list.instances(), nil
}

// ListLocations returns every Location visible to the project.
func (c *Client) ListLocations(ctx context.Context) ([]geo.Place, error) {
	var list locationList
	if err := c.get(ctx, "/apis/locations.miloapis.com/v1alpha1/locations", &list); err != nil {
		return nil, err
	}
	return list.places(), nil
}

// ParseInstanceList decodes an InstanceList JSON document.
func ParseInstanceList(data []byte) ([]Instance, error) {
	var list instanceList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list.instances(), nil
}

type condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	LastTransitionTime string `json:"lastTransitionTime"`
}

type instanceList struct {
	Items []struct {
		Metadata struct {
			Name              string            `json:"name"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
			DeletionTimestamp string            `json:"deletionTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Location *struct {
				Name string `json:"name"`
			} `json:"location"`
		} `json:"spec"`
		Status struct {
			Conditions        []condition `json:"conditions"`
			NetworkInterfaces []struct {
				Addresses []struct {
					Address string `json:"address"`
					Family  string `json:"family"`
					Primary bool   `json:"primary"`
				} `json:"addresses"`
				Assignments struct {
					NetworkIP string `json:"networkIP"`
				} `json:"assignments"`
			} `json:"networkInterfaces"`
		} `json:"status"`
	} `json:"items"`
}

func (l instanceList) instances() []Instance {
	out := make([]Instance, 0, len(l.Items))
	for _, item := range l.Items {
		inst := Instance{
			Name:     item.Metadata.Name,
			Location: item.Metadata.Labels[labelLocation],
		}
		if inst.Location == "" && item.Spec.Location != nil {
			inst.Location = item.Spec.Location.Name
		}
		inst.CreatedAt, _ = time.Parse(time.RFC3339, item.Metadata.CreationTimestamp)
		inst.StoppingAt, _ = time.Parse(time.RFC3339, item.Metadata.DeletionTimestamp)
		inst.Stopping = !inst.StoppingAt.IsZero()

		for _, c := range item.Status.Conditions {
			if c.Type != "Available" {
				continue
			}
			inst.Available = c.Status == "True"
			inst.StatusReason = c.Reason
			inst.AvailableAt, _ = time.Parse(time.RFC3339, c.LastTransitionTime)
		}

		// The first interface is the one attached to the workload's network.
		// Within it, a primary address wins, then any address, then the
		// assignment recorded before addresses are reported.
		if len(item.Status.NetworkInterfaces) > 0 {
			nic := item.Status.NetworkInterfaces[0]
			var candidates []string
			for _, a := range nic.Addresses {
				if a.Primary {
					candidates = append(candidates, a.Address)
				}
			}
			for _, a := range nic.Addresses {
				candidates = append(candidates, a.Address)
			}
			candidates = append(candidates, nic.Assignments.NetworkIP)
			for _, cand := range candidates {
				if prefix, ok := ParseAddress(cand); ok {
					inst.Prefix = prefix
					inst.PrivateIP = prefix.Addr()
					break
				}
			}
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ParseAddress accepts an address as the API reports it, either a bare IP or
// CIDR notation such as fd20:0:f::1:0:0/96, and returns it as a prefix whose
// Addr is the instance's own IP.
func ParseAddress(s string) (netip.Prefix, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Prefix{}, false
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		return p, true
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
}

type locationList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Topology    map[string]string `json:"topology"`
			Coordinates *struct {
				Latitude  string `json:"latitude"`
				Longitude string `json:"longitude"`
			} `json:"coordinates"`
		} `json:"spec"`
	} `json:"items"`
}

func (l locationList) places() []geo.Place {
	out := make([]geo.Place, 0, len(l.Items))
	for _, item := range l.Items {
		p := geo.Place{
			Name:        item.Metadata.Name,
			CityCode:    item.Spec.Topology[topologyCityCode],
			City:        item.Spec.Topology[topologyCity],
			Country:     item.Spec.Topology[topologyCountry],
			CountryCode: item.Spec.Topology[topologyCountryCode],
		}
		if c := item.Spec.Coordinates; c != nil {
			lat, errLat := strconv.ParseFloat(c.Latitude, 64)
			lon, errLon := strconv.ParseFloat(c.Longitude, 64)
			if errLat == nil && errLon == nil {
				p.Lat, p.Lon = lat, lon
			}
		}
		out = append(out, p)
	}
	return out
}
