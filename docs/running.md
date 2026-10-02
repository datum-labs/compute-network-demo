# Running and configuring the demo

## Building from source

The web build writes into `internal/site/dist`, where the Go binary embeds it,
so it has to run first. You need Go 1.26+ and Node 22+.

```sh
(cd web && npm install && npm run build)
DEMO_MODE=simulate go run .
```

To work on the page with hot reload, keep the Go server running and start Vite
in a second terminal. It proxies `/api` and `/mesh` back to port 8080.

```sh
cd web && npm run dev    # http://localhost:5173
```

```sh
go vet ./... && go test ./...
```

## Simulate mode and live mode

The demo has two ways of knowing what the fleet looks like, and they produce
the same page. Nothing in `web/` knows which one is underneath.

**Simulate mode** (`DEMO_MODE=simulate`) models a fleet across the three launch
locations — us-central-1 (Dallas), us-east-1 (Ashburn) and us-west-1 (San Jose)
— with latencies worked out from the real distances between them, and a
workload that scales itself in each. Dallas breathes between two and five
Instances on an 88-second cycle, San Jose runs a shorter cycle on a different
phase, and Ashburn stays steady enough to read as a reference, so anyone
watching for a minute sees both a scale-up and a scale-down. Every Instance
starts, joins the mesh and later drains on a lifecycle derived from its name,
so the same fleet behaves the same way on every run. The page says `SIMULATED`
in the corner while this is on.

**Live mode** (`DEMO_MODE=live`) discovers the real Instances of a real
workload through the Datum Cloud API and measures real round-trip times between
them over the private network. It needs a project, a service account with read
access to compute resources, and a route from the private network to the API.
See [`deploy/README.md`](../deploy/README.md).

## Settings

| Variable | Default | Purpose |
| --- | --- | --- |
| `DEMO_MODE` | `live` | `simulate` models a fleet; `live` discovers real Instances |
| `DEMO_CHURN` | `on` | Simulate: each location scales with demand; `off` pins every location at its floor |
| `DEMO_FAULTS` | `off` | Simulate: breaks one Dallas–Ashburn pair, to show a degraded link |
| `DATUM_PROJECT` | | Live: project to discover Instances in |
| `DATUM_WORKLOAD` | `global-mesh` | Live: workload name to discover |
| `DATUM_CREDENTIALS_FILE` | `/etc/datum/credentials.json` | Live: service-account key JSON |
| `DATUM_API_URL` | | Live: Datum Cloud API endpoint, required |
| `DATUM_AUTH_URL` | | Live: auth server, for OIDC discovery, required |
| `MESH_PEERS` | | Live: fallback peer list, `us-central-1=fd20:…,us-east-1=fd20:…` |
| `MESH_DRIVER` | `off` | Live: `on` lets one replica scale this workload (see below) |
| `MESH_DRIVER_CREDENTIALS_FILE` | `/etc/datum-driver/credentials.json` | Live: the driver's own service-account key |
| `MESH_DRIVER_MAX` | `3` | Live: most Instances the driver will run in one city |
| `MESH_DRIVER_INTERVAL` | `4m` | Live: shortest gap between two scaling changes |
| `MESH_DRIVER_SETTLE_TIMEOUT` | `6m` | Live: how long a scale-up has to join the mesh before it is undone |
| `MESH_SELF` | | Override this Instance's name, normally detected from its address |
| `MESH_PORT` | `8080` | Port peers listen on |
| `MESH_PING_INTERVAL` | `2s` | How often each peer is messaged |
| `LISTEN_ADDR` | `[::]:8080` | Listen address, dual-stack |

## Making a live fleet scale itself

A real workload sitting at one Instance per city has nothing for the narration
to say. With `MESH_DRIVER=on`, exactly one replica of the workload changes the
workload's own shape through the Datum Cloud API, slowly, so the activity feed
carries real scale-ups, real Instances joining the mesh, and real drains.

It is off by default, and it is the only thing in the demo that writes.

- **Which replica drives.** The lowest-named Instance that discovery currently
  sees running. That is decided again on every discovery tick, from the fleet
  the page is already looking at, so no lease or external coordination is
  needed. Two replicas briefly agreeing they both drive is harmless: every
  action writes a desired shape, and writing it twice is the same as writing it
  once. A replica that loses a race gets a `409` and waits for its next tick.
- **How it scales.** By adding and removing placements, not by moving an
  existing placement's `minReplicas`. A new placement named `<base>-2` copies
  the base placement's `locationSelector` and asks for one Instance, so the city
  grows where it already is; scaling down removes the highest-numbered extra.
  Base placements are never removed, and a city whose base placement is not
  available is left alone entirely.
- **How slowly.** One change at a time, no sooner than `MESH_DRIVER_INTERVAL`
  after the last one — or, for a replica that has just taken the fleet, after it
  started driving, so a rollout waits a full interval before its first change —
  and only when the fleet is settled: every Instance
  running, none draining, and every link up. After a scale-up nothing else
  happens until the new Instance is in the mesh and reachable by every peer. If
  that has not happened within `MESH_DRIVER_SETTLE_TIMEOUT` the placement is
  removed again and the driver backs off for three intervals.
- **What it does.** A gentle staircase: one city up, then another city up, then
  each back down, so no two cities ever move together and the fleet returns to
  its baseline between cycles. Each cycle starts with a different city. A city
  never drops below one Instance or rises above `MESH_DRIVER_MAX`.

### The driver's own identity

The driver authenticates as a **second** service account, read from
`MESH_DRIVER_CREDENTIALS_FILE`, so the account the page discovers with stays
read-only. Set it up the way `deploy/live/00-serviceaccount.yaml` and
`deploy/live/10-policybinding.yaml` do for the read-only account, with two
differences: a different name, and the `a role that can patch Workloads (staging currently only allows the project editor role; narrow it when Milo permits a compute-scoped role)` role in
place of `compute.datumapis.com-viewer`. Then mount its key as a second Secret
and uncomment the `MESH_DRIVER` block in `deploy/live/30-workload.yaml`.

If `MESH_DRIVER=on` and that file is missing or unreadable, the demo logs one
error and runs read-only rather than refusing to start — the page is the point,
and the driver only makes it livelier.

Every decision is logged at INFO with the city, the action, the placement and
why, and the current state is on `/api/mesh` under `driver`:

```sh
curl -s https://<hostname>/api/mesh | jq .driver
```

## The guided walkthrough

The page explains itself in two phases. The **intro** is four hand-written
cards, each naming something Datum does and then pointing at the thing on
screen that proves it; it has Back and Next, because nothing is moving while it
runs. **Live narration** takes over afterwards and raises a card each time the
workload does something — a location scales, an Instance joins the network, an
Instance drains. Those cards have no navigation, because a scale-up cannot be
rewound.

![The intro, inside a location](story-1.png)

Append `?story=` to the URL to change that.

| `?story=` | What happens |
| --- | --- |
| *(omitted)* | The intro plays once on arrival, then live narration takes over. |
| `on` | The same as omitting it. |
| `loop` | The intro plays on repeat and never hands over. Use this for a booth or a kiosk. |
| `off` | No intro; live narration still runs. |
| `quiet` | Nothing at all. Use this for screenshots. |

Steering the map — a zoom, a pan, a hover — hands the camera to the visitor and
pauses the intro's clock, but keeps the words. The camera comes back, and a
paused intro carries on, once the map has been left alone for twenty seconds,
so a booth screen recovers from a passer-by. Only the card's close button ends
the walkthrough, and "Replay the tour" under the map starts it again. With
`prefers-reduced-motion: reduce`, the camera cuts between framings instead of
gliding and the cards fade rather than travel.

Take screenshots with `?story=quiet`, so neither the intro nor a narration card
lands under them.

![Live narration, an Instance joining](story-live.png)

The page is built for phones as well as projectors. Which layout appears is
decided by the shape of the space the map would get rather than the window's
pixel count, and the map always fills the box it is given, so there are never
bands of empty ocean.

![On a phone](screenshot-mobile.png)

## How it works

Every Instance runs the same static binary, and that binary contains the page.

1. **Discovery.** Every 5 seconds the Instance lists its workload's Instances
   through the Datum Cloud API, authenticating the way `datumctl login
   --credentials` does. It works out which Instance it is by matching its own
   network addresses. City names and coordinates come from the Locations API
   when the identity may list it (the compute viewer role alone gets a 403),
   with a built-in table as a fallback. If the API cannot be reached it falls
   back to the `MESH_PEERS` list, and the page shows a "Static peers" marker so
   it never quietly misrepresents where its view came from.
2. **Traffic.** Every 2 seconds each Instance sends a small request to
   `/mesh/ping` on every peer's private address, and records the median round
   trip, the success rate and the bytes on the wire. Each Instance keeps its
   last fifty exchanges, which is what the live feed is built from.
3. **Aggregation.** When a browser asks any Instance for `/api/mesh`, that
   Instance collects every peer's measurements from `/mesh/local` over the
   private network, in parallel, with short timeouts. So the page shows the
   whole mesh whichever Instance served it. An unreachable peer never breaks
   the view — its arcs turn red.

```
browser ──HTTPProxy──▶ nearest Instance ──/mesh/local──▶ every peer (private network)
                              │
                              └── Datum Cloud API: list Instances, Locations
```

The Go module has no dependencies outside the standard library and builds with
`CGO_ENABLED=0` into one static binary. The page is React 19, TypeScript,
Tailwind v4 and `@datum-cloud/datum-ui`, built by Vite. The dotted world map
and its projection come from the Datum Cloud portal's edge overview.

`web/src/narration.ts` reads nothing but the activity event stream that the
server builds by diffing successive observations of the fleet — the same code
path for a simulated fleet and a real one, so a live workload needs nothing
added there. The intro in `web/src/intro.ts` is hand-written explanation and
always will be, which is why the two live in separate modules.
