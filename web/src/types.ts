/** Mirrors the JSON served by GET /api/mesh (internal/mesh/model.go). */

export type EdgeState = 'up' | 'degraded' | 'down' | 'pending';

/** Where an Instance is in its life: arriving, working, or draining. */
export type InstanceStatus = 'starting' | 'running' | 'stopping';

/** What the platform did on its own, as shown in the activity feed. */
export type ActivityType =
  | 'scaled-up'
  | 'scaled-down'
  | 'instance-starting'
  | 'instance-ready'
  | 'instance-stopping';

export interface Activity {
  at: string;
  type: ActivityType;
  location: string;
  city?: string;
  instance?: string;
  /** Instance counts either side of a scaling decision. */
  from?: number;
  to?: number;
  /** How long the Instance took to become reachable by every peer. */
  joinMs?: number;
}

export interface InstanceView {
  name: string;
  location: string;
  cityCode: string;
  city: string;
  country: string;
  countryCode: string;
  lat: number;
  lon: number;
  privateIP: string;
  status: InstanceStatus;
  isSelf: boolean;
  uptimeSeconds: number;
  peersReachable: number;
  peersTotal: number;
  reporting: boolean;
  /** How long this Instance took to become reachable by every peer. */
  joinMs?: number;
}

export interface EdgeView {
  from: string;
  to: string;
  rttMs: number;
  successRate: number;
  messages: number;
  bytes: number;
  state: EdgeState;
}

/** One message an instance sent to a peer, as shown in the live feed. */
export interface Exchange {
  at: string;
  from: string;
  to: string;
  rttMs: number;
  ok: boolean;
}

export interface MeshView {
  mode: 'live' | 'simulate';
  /** How instances were found: the Datum Cloud API or a static MESH_PEERS list. */
  discovery?: 'datum-api' | 'static' | 'simulated';
  generatedAt: string;
  project?: string;
  workload?: string;
  self: string;
  instances: InstanceView[];
  edges: EdgeView[];
  exchanges: Exchange[];
  activity: Activity[];
  totals: {
    regions: number;
    instances: number;
    messages: number;
    bytes: number;
    avgRttMs: number;
    publicInternetBytes: number;
  };
  notice?: string;
}

/** A city on the map: every instance of the workload in one location. */
export interface Region {
  location: string;
  city: string;
  cityCode: string;
  country: string;
  countryCode: string;
  lat: number;
  lon: number;
  x: number;
  y: number;
  isSelf: boolean;
  instances: InstanceView[];
}

/** Traffic between two regions, both directions combined. */
export interface Link {
  key: string;
  a: Region;
  b: Region;
  rttMs: number;
  state: EdgeState;
  messages: number;
}

/** One replica of the workload as it is drawn when its location is expanded. */
export interface Replica {
  instance: InstanceView;
  region: Region;
  /** 1-based position within its location, for a short human label. */
  ordinal: number;
  x: number;
  y: number;
}

/** Traffic between two replicas sharing a location, both directions combined. */
export interface IntraLink {
  key: string;
  location: string;
  from: string;
  to: string;
  rttMs: number;
  state: EdgeState;
  /** True while either end is draining, so the link can be drawn winding down. */
  draining: boolean;
}
