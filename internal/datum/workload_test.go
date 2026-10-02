package datum

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestParseWorkloadProductionShape(t *testing.T) {
	data, err := os.ReadFile("testdata/workload.json")
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWorkload(data)
	if err != nil {
		t.Fatal(err)
	}
	if w.Name != "mesh-demo-real-mesh" {
		t.Errorf("name = %q", w.Name)
	}
	if w.ResourceVersion == "" {
		t.Error("resourceVersion not parsed")
	}
	if len(w.Placements) != 2 {
		t.Fatalf("got %d placements, want 2", len(w.Placements))
	}
	dfw := w.Placements[0]
	if dfw.Name != "dfw" {
		t.Errorf("placement 0 name = %q", dfw.Name)
	}
	labels, ok := dfw.LocationSelector["matchLabels"].(map[string]any)
	if !ok || labels["topology.datum.net/city-code"] != "DFW" {
		t.Errorf("locationSelector = %+v", dfw.LocationSelector)
	}
	if dfw.ScaleSettings["minReplicas"] != float64(1) {
		t.Errorf("scaleSettings = %+v", dfw.ScaleSettings)
	}
	// The driver refuses to touch a city whose placement is not healthy, so
	// the per-placement status has to survive parsing.
	if len(w.PlacementStatus) != 2 || !w.PlacementStatus[0].Available {
		t.Fatalf("placement status = %+v", w.PlacementStatus)
	}
	if !w.PlacementReady("dfw") || w.PlacementReady("nope") {
		t.Error("PlacementReady disagrees with the fixture")
	}
}

// A placement the driver copies has to round-trip whole: a field it does not
// know about, such as instanceManagementPolicy, must come back unchanged.
func TestPlacementRoundTripsUnknownFields(t *testing.T) {
	data, err := os.ReadFile("testdata/workload.json")
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWorkload(data)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(w.Placements[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	scale := got["scaleSettings"].(map[string]any)
	if scale["instanceManagementPolicy"] != "OrderedReady" {
		t.Errorf("placement lost scaleSettings fields: %s", out)
	}
}

func TestGetWorkload(t *testing.T) {
	fixture, err := os.ReadFile("testdata/workload.json")
	if err != nil {
		t.Fatal(err)
	}
	const path = "/apis/resourcemanager.miloapis.com/v1alpha1/projects/demo-project/control-plane" +
		"/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads/mesh-demo-real-mesh"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	c := &Client{APIURL: srv.URL, Project: "demo-project", Tokens: staticToken("tok"), HTTP: srv.Client()}
	got, err := c.GetWorkload(context.Background(), "mesh-demo-real-mesh")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Placements) != 2 {
		t.Fatalf("got %d placements", len(got.Placements))
	}
}

func TestPatchWorkloadPlacements(t *testing.T) {
	var gotBody []byte
	var gotType, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"metadata":{"name":"mesh"}}`))
	}))
	defer srv.Close()

	c := &Client{APIURL: srv.URL, Project: "demo-project", Tokens: staticToken("tok"), HTTP: srv.Client()}
	placements := []Placement{{Name: "dfw", ScaleSettings: map[string]any{"minReplicas": 1}}}
	if err := c.PatchWorkloadPlacements(context.Background(), "mesh", placements, "42"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotType != "application/merge-patch+json" {
		t.Errorf("content-type = %q", gotType)
	}
	var patch struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Placements []Placement `json:"placements"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(gotBody, &patch); err != nil {
		t.Fatalf("patch body %s: %v", gotBody, err)
	}
	if patch.Metadata.ResourceVersion != "42" {
		t.Errorf("resourceVersion = %q, want 42", patch.Metadata.ResourceVersion)
	}
	if len(patch.Spec.Placements) != 1 || patch.Spec.Placements[0].Name != "dfw" {
		t.Errorf("placements = %+v", patch.Spec.Placements)
	}
}

// Another driver writing first is normal, not an error worth retrying hard.
func TestPatchWorkloadPlacementsConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"kind":"Status","reason":"Conflict"}`, http.StatusConflict)
	}))
	defer srv.Close()

	c := &Client{APIURL: srv.URL, Project: "demo-project", Tokens: staticToken("tok"), HTTP: srv.Client()}
	err := c.PatchWorkloadPlacements(context.Background(), "mesh", nil, "1")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}
