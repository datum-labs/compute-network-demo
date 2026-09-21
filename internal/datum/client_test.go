package datum

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"testing"
)

func TestParseInstanceListProductionShape(t *testing.T) {
	data, err := os.ReadFile("testdata/instances.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseInstanceList(data)
	if err != nil {
		t.Fatal(err)
	}

	want := []struct {
		name, location, ip string
		available          bool
	}{
		{"gp-http-proof-default-us-central-1-0", "us-central-1", "fd20:0:f::1:0:0", true},
		{"xcheck-dfw-us-central-1-0", "us-central-1", "fd20:0:2::3:0:0", true},
		{"xcheck-iad-us-east-1-0", "us-east-1", "fd20:0:2:1:0:2::", false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d instances, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Name != w.name || g.Location != w.location || g.PrivateIP.String() != w.ip || g.Available != w.available {
			t.Errorf("instance %d = {%s %s %s %v}, want %+v", i, g.Name, g.Location, g.PrivateIP, g.Available, w)
		}
		if g.Prefix.Bits() != 96 {
			t.Errorf("%s prefix bits = %d, want 96", g.Name, g.Prefix.Bits())
		}
		if g.CreatedAt.IsZero() {
			t.Errorf("%s has no creation time", g.Name)
		}
	}
	if got[0].AvailableAt.IsZero() {
		t.Error("available transition time not parsed")
	}
}

func TestParseInstanceFallsBackToAssignment(t *testing.T) {
	doc := `{"items":[{"metadata":{"name":"a","labels":{}},"spec":{"location":{"name":"us-east-1"}},
	  "status":{"networkInterfaces":[{"assignments":{"networkIP":"fd20:0:2:1:0:2::/96"}}]}}]}`
	got, err := ParseInstanceList([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].PrivateIP.String() != "fd20:0:2:1:0:2::" || got[0].Location != "us-east-1" {
		t.Fatalf("got %+v", got[0])
	}
}

func TestParseInstanceWithoutNetwork(t *testing.T) {
	got, err := ParseInstanceList([]byte(`{"items":[{"metadata":{"name":"pending"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].PrivateIP.IsValid() {
		t.Fatalf("expected no address, got %s", got[0].PrivateIP)
	}
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in, ip string
		bits   int
		ok     bool
	}{
		{"fd20:0:f::1:0:0/96", "fd20:0:f::1:0:0", 96, true},
		{"fd20:0:2:1:0:2::/96", "fd20:0:2:1:0:2::", 96, true},
		{"10.0.0.7/24", "10.0.0.7", 24, true},
		{"10.0.0.7", "10.0.0.7", 32, true},
		{"::ffff:10.0.0.7", "10.0.0.7", 32, true},
		{"fd20::5", "fd20::5", 128, true},
		{"", "", 0, false},
		{"not-an-ip/96", "", 0, false},
	}
	for _, c := range cases {
		p, ok := ParseAddress(c.in)
		if ok != c.ok {
			t.Errorf("ParseAddress(%q) ok = %v", c.in, ok)
			continue
		}
		if ok && (p.Addr() != netip.MustParseAddr(c.ip) || p.Bits() != c.bits) {
			t.Errorf("ParseAddress(%q) = %s", c.in, p)
		}
	}
}

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

func TestClientRequests(t *testing.T) {
	fixture, err := os.ReadFile("testdata/instances.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		base := "/apis/resourcemanager.miloapis.com/v1alpha1/projects/demo-project/control-plane"
		switch r.URL.Path {
		case base + "/apis/compute.datumapis.com/v1alpha/namespaces/default/instances":
			if r.URL.Query().Get("labelSelector") != "compute.datumapis.com/workload-name=global-mesh" {
				http.Error(w, "bad selector", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(fixture)
		case base + "/apis/locations.miloapis.com/v1alpha1/locations":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"us-central-1"},"spec":{
			  "topology":{"topology.datum.net/city-code":"DFW","topology.datum.net/city":"Dallas","topology.datum.net/country":"United States"},
			  "coordinates":{"latitude":"32.7767","longitude":"-96.7970"}}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{APIURL: srv.URL, Project: "demo-project", Tokens: staticToken("tok"), HTTP: srv.Client()}
	insts, err := c.ListInstances(context.Background(), "global-mesh")
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 3 {
		t.Fatalf("got %d instances", len(insts))
	}
	places, err := c.ListLocations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 1 || places[0].City != "Dallas" || places[0].Lat != 32.7767 || places[0].CityCode != "DFW" {
		t.Fatalf("places = %+v", places)
	}
}
