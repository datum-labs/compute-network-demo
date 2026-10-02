package datum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrConflict reports that the object changed between the read and the write.
// For the fleet driver that means another replica wrote first, which is an
// expected outcome rather than a failure.
var ErrConflict = errors.New("resource version conflict")

// Workload is the part of a compute Workload the fleet driver needs: the
// placements it may rewrite, the resource version that guards the write, and
// enough status to tell a healthy placement from a broken one.
type Workload struct {
	Name            string
	ResourceVersion string
	Placements      []Placement
	PlacementStatus []PlacementStatus
}

// Placement is one entry of spec.placements. The selector and the scale
// settings are carried as parsed JSON so a placement survives a round trip
// through a field this demo does not know about.
type Placement struct {
	Name             string         `json:"name"`
	LocationSelector map[string]any `json:"locationSelector,omitempty"`
	ScaleSettings    map[string]any `json:"scaleSettings,omitempty"`
}

// PlacementStatus is what the control plane reports for one placement.
type PlacementStatus struct {
	Name string
	// Available is the placement's Available condition. The control plane has
	// no Ready condition on a placement; Available is the one that says cells
	// are acting on it.
	Available       bool
	Replicas        int
	ReadyReplicas   int
	CurrentReplicas int
}

// PlacementReady reports whether the named placement is available. An unknown
// placement is not ready, so a caller never grows a city the control plane has
// not acknowledged.
func (w *Workload) PlacementReady(name string) bool {
	for _, s := range w.PlacementStatus {
		if s.Name == name {
			return s.Available
		}
	}
	return false
}

// GetWorkload reads one Workload from the project's control plane.
func (c *Client) GetWorkload(ctx context.Context, name string) (*Workload, error) {
	var doc workloadDoc
	if err := c.get(ctx, workloadPath(name), &doc); err != nil {
		return nil, err
	}
	return doc.workload(), nil
}

// PatchWorkloadPlacements replaces spec.placements, carrying resourceVersion
// so a concurrent write loses rather than being silently overwritten. A merge
// patch replaces a list wholesale, which is what a new placement set wants.
func (c *Client) PatchWorkloadPlacements(ctx context.Context, name string, placements []Placement, resourceVersion string) error {
	if placements == nil {
		// A merge patch reads a missing key as "leave alone" and null as
		// "delete", and neither is ever the intent here.
		placements = []Placement{}
	}
	patch := map[string]any{
		"metadata": map[string]any{"resourceVersion": resourceVersion},
		"spec":     map[string]any{"placements": placements},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return c.send(ctx, http.MethodPatch, workloadPath(name), "application/merge-patch+json", body)
}

func workloadPath(name string) string {
	return "/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads/" + url.PathEscape(name)
}

func (c *Client) send(ctx context.Context, method, path, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, c.projectBase()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("%s %s: %w", method, path, ErrConflict)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(reply))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, msg)
	}
	return nil
}

// ParseWorkload decodes a Workload JSON document.
func ParseWorkload(data []byte) (*Workload, error) {
	var doc workloadDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc.workload(), nil
}

type workloadDoc struct {
	Metadata struct {
		Name            string `json:"name"`
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Spec struct {
		Placements []Placement `json:"placements"`
	} `json:"spec"`
	Status struct {
		Placements []struct {
			Name            string      `json:"name"`
			Conditions      []condition `json:"conditions"`
			Replicas        int         `json:"replicas"`
			ReadyReplicas   int         `json:"readyReplicas"`
			CurrentReplicas int         `json:"currentReplicas"`
		} `json:"placements"`
	} `json:"status"`
}

func (d workloadDoc) workload() *Workload {
	w := &Workload{
		Name:            d.Metadata.Name,
		ResourceVersion: d.Metadata.ResourceVersion,
		Placements:      d.Spec.Placements,
	}
	for _, p := range d.Status.Placements {
		s := PlacementStatus{
			Name:            p.Name,
			Replicas:        p.Replicas,
			ReadyReplicas:   p.ReadyReplicas,
			CurrentReplicas: p.CurrentReplicas,
		}
		for _, c := range p.Conditions {
			if c.Type == "Available" {
				s.Available = c.Status == "True"
			}
		}
		w.PlacementStatus = append(w.PlacementStatus, s)
	}
	return w
}
