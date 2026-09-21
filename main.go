// Command global-mesh serves the Datum Global Mesh demo. Every instance of the
// workload runs this same binary: it discovers its peers through the Datum
// Cloud API, exchanges small messages with each of them over the private
// network, and serves a live map of the whole mesh.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/datum-labs/compute-network-demo/internal/auth"
	"github.com/datum-labs/compute-network-demo/internal/datum"
	"github.com/datum-labs/compute-network-demo/internal/geo"
	"github.com/datum-labs/compute-network-demo/internal/mesh"
	"github.com/datum-labs/compute-network-demo/internal/site"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()})))
	if err := run(); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listen := env("LISTEN_ADDR", "[::]:8080")
	port := envInt("MESH_PORT", 8080)
	workload := env("DATUM_WORKLOAD", "global-mesh")
	dir := geo.NewDirectory()

	var src mesh.Source
	switch mode := env("DEMO_MODE", "live"); mode {
	case "simulate":
		slog.Info("running with a simulated fleet")
		src = mesh.NewSimulator(workload, dir, env("DEMO_CHURN", "on") != "off", env("DEMO_FAULTS", "off") == "on")
	case "live":
		live, err := newLive(workload, port, dir)
		if err != nil {
			return err
		}
		live.Start(ctx)
		src = live
	default:
		return errors.New("DEMO_MODE must be live or simulate")
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           mesh.NewHandler(src, site.FS()),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("listening", "addr", listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newLive(workload string, port int, dir *geo.Directory) (*mesh.Live, error) {
	staticPeers, err := mesh.ParseStaticPeers(os.Getenv("MESH_PEERS"))
	if err != nil {
		return nil, err
	}

	interval := envDuration("MESH_PING_INTERVAL", 2*time.Second)
	live := &mesh.Live{
		StaticPeers:       staticPeers,
		SelfName:          os.Getenv("MESH_SELF"),
		Workload:          workload,
		Port:              port,
		Pinger:            mesh.NewPinger(port, interval, 1500*time.Millisecond),
		Directory:         dir,
		DiscoverEvery:     envDuration("MESH_DISCOVER_INTERVAL", 5*time.Second),
		LocationsEvery:    time.Minute,
		CacheFor:          time.Second,
		PeerReportTimeout: 1200 * time.Millisecond,
	}

	// The Datum Cloud API is the primary discovery source. MESH_PEERS covers
	// networks with no route to it, and is used whenever the API is absent or
	// unreachable.
	credsPath := env("DATUM_CREDENTIALS_FILE", "/etc/datum/credentials.json")
	creds, credsErr := auth.LoadCredentials(credsPath)
	project := os.Getenv("DATUM_PROJECT")
	switch {
	case credsErr == nil && project != "":
		// No defaults: the endpoints differ per environment, and guessing one
		// would send a service-account assertion to the wrong place.
		apiURL, authURL := os.Getenv("DATUM_API_URL"), os.Getenv("DATUM_AUTH_URL")
		if apiURL == "" || authURL == "" {
			return nil, errors.New("DATUM_API_URL and DATUM_AUTH_URL are required to discover instances through the Datum Cloud API (or set MESH_PEERS, or DEMO_MODE=simulate)")
		}
		httpClient := &http.Client{Timeout: 15 * time.Second}
		tokens := auth.NewTokenSource(creds, authURL, httpClient)
		live.API = &datum.Client{
			APIURL:  apiURL,
			Project: project,
			Tokens:  tokens,
			HTTP:    httpClient,
		}
		live.Project = project
	case len(staticPeers) == 0 && credsErr != nil:
		return nil, fmt.Errorf("no discovery source: %w (set MESH_PEERS, or DEMO_MODE=simulate)", credsErr)
	case len(staticPeers) == 0:
		return nil, errors.New("DATUM_PROJECT is required to discover instances through the Datum Cloud API (or set MESH_PEERS, or DEMO_MODE=simulate)")
	default:
		slog.Info("Datum Cloud API discovery unavailable, using MESH_PEERS", "reason", reason(credsErr, project), "peers", len(staticPeers))
	}
	return live, nil
}

func reason(credsErr error, project string) string {
	if credsErr != nil {
		return credsErr.Error()
	}
	if project == "" {
		return "DATUM_PROJECT not set"
	}
	return ""
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil && d > 0 {
		return d
	}
	return def
}

func logLevel() slog.Level {
	if os.Getenv("LOG_LEVEL") == "debug" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
