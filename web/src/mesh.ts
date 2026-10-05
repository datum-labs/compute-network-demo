import { geoMercator } from 'd3-geo';
import type { EdgeState, IntraLink, Link, MeshView, Region } from './types';

/** Intrinsic size of public/world-map-dots.svg. */
export const MAP_WIDTH = 1038;
export const MAP_HEIGHT = 591;

// Dot centers in world-map-dots.svg, keyed by Location name, for every
// Location in the Locations API (locations.miloapis.com). The map is
// hand-drawn art, so a projection lands some coastal cities in the ocean;
// pinning each known Location to a dot keeps it on land. Re-pick these if
// world-map-dots.svg changes.
const LOCATION_DOTS: Record<string, [number, number]> = {
  'ae-north-1': [661.722, 320.075], // Dubai
  'au-east-1': [953.38, 503.395], // Sydney
  'br-east-1': [345.071, 461.735], // São Paulo
  'ca-east-1': [253.409, 236.739], // Toronto
  'cl-central-1': [286.743, 495.055], // Santiago
  'de-central-1': [528.397, 211.743], // Frankfurt
  'gb-south-1': [486.735, 203.411], // London
  'in-west-1': [728.392, 328.403], // Mumbai
  'jp-east-1': [911.716, 253.407], // Tokyo
  'nl-west-1': [520.063, 203.411], // Amsterdam
  'sg-central-1': [811.722, 386.731], // Singapore
  'us-central-1': [203.413, 286.735], // Dallas
  'us-east-1': [261.739, 261.739], // Ashburn
  'us-east-2': [270.077, 253.407], // New York
  'us-west-1': [136.749, 253.407], // San Jose
  'za-central-1': [578.394, 470.063], // Johannesburg
};

// Fallback for Locations without a pinned dot. The map is roughly Mercator but
// doesn't span a full 360° of longitude, so it can't be fit from bounds; these
// values were fitted against Natural Earth coastlines.
const projection = geoMercator().scale(174.5).translate([496.5, 389.5]);

export function project(lat: number, lon: number, location?: string): [number, number] {
  const dot = location ? LOCATION_DOTS[location] : undefined;
  if (dot) return dot;
  return projection([lon, lat]) ?? [MAP_WIDTH / 2, MAP_HEIGHT / 2];
}

export function buildRegions(view: MeshView | null): Region[] {
  if (!view) return [];
  const byLocation = new Map<string, Region>();
  for (const inst of view.instances) {
    const key = inst.location || inst.name;
    let region = byLocation.get(key);
    if (!region) {
      const [x, y] = project(inst.lat, inst.lon, inst.location);
      region = {
        location: key,
        city: inst.city || key,
        cityCode: inst.cityCode,
        country: inst.country,
        countryCode: inst.countryCode,
        lat: inst.lat,
        lon: inst.lon,
        x,
        y,
        isSelf: false,
        instances: [],
      };
      byLocation.set(key, region);
    }
    region.instances.push(inst);
    region.isSelf ||= inst.isSelf;
  }
  for (const region of byLocation.values()) {
    region.instances.sort((a, b) => a.name.localeCompare(b.name));
  }
  return [...byLocation.values()].sort((a, b) => a.location.localeCompare(b.location));
}

/** Key shared by the map and the live feed for one pair of replicas. */
export const intraLinkKey = (a: string, b: string) => (a < b ? `i:${a}|${b}` : `i:${b}|${a}`);

/**
 * Links between replicas sharing a location. buildLinks folds these into the
 * region they belong to, but they are the fastest traffic in the fleet and the
 * reason a location is worth zooming into, so they are kept separately.
 */
export function buildIntraLinks(view: MeshView | null, regions: Region[]): IntraLink[] {
  if (!view) return [];
  const locationOf = new Map<string, string>();
  for (const r of regions) for (const i of r.instances) locationOf.set(i.name, r.location);

  const draining = new Set(view.instances.filter((i) => i.status === 'stopping').map((i) => i.name));

  const acc = new Map<string, { link: IntraLink; states: EdgeState[]; rtts: number[] }>();
  for (const e of view.edges) {
    const a = locationOf.get(e.from);
    if (!a || a !== locationOf.get(e.to)) continue;
    const key = intraLinkKey(e.from, e.to);
    const entry = acc.get(key) ?? {
      link: {
        key,
        location: a,
        from: e.from < e.to ? e.from : e.to,
        to: e.from < e.to ? e.to : e.from,
        rttMs: 0,
        state: 'pending' as EdgeState,
        draining: draining.has(e.from) || draining.has(e.to),
      },
      states: [],
      rtts: [],
    };
    entry.states.push(e.state);
    if (e.rttMs > 0 && e.state !== 'down') entry.rtts.push(e.rttMs);
    acc.set(key, entry);
  }

  return [...acc.values()].map(({ link, states, rtts }) => ({
    ...link,
    rttMs: rtts.length ? rtts.reduce((s, v) => s + v, 0) / rtts.length : 0,
    state: combineStates(states),
  }));
}

/**
 * Places a location's replicas around its anchor, measured in screen pixels so
 * they stay clear of each other however far the map is zoomed.
 */
export function fanOffsets(count: number, radiusPx: number): [number, number][] {
  if (count <= 1) return [[0, 0]];
  const step = (Math.PI * 2) / count;
  // Start at the top, so an even count never straddles the city's name.
  return Array.from({ length: count }, (_, i) => {
    const angle = -Math.PI / 2 + i * step;
    return [Math.cos(angle) * radiusPx, Math.sin(angle) * radiusPx] as [number, number];
  });
}

function combineStates(states: EdgeState[]): EdgeState {
  const measured = states.filter((s) => s !== 'pending');
  if (measured.length === 0) return 'pending';
  if (measured.every((s) => s === 'up')) return 'up';
  if (measured.every((s) => s === 'down')) return 'down';
  return 'degraded';
}

/**
 * Collapses directed instance edges into one link per pair of regions.
 * Every pair of regions gets a link, so a newly joined region draws its arcs
 * right away and shows them as pending until traffic is measured.
 */
export function buildLinks(view: MeshView | null, regions: Region[]): Link[] {
  if (!view) return [];
  const regionOf = new Map<string, Region>();
  for (const r of regions) for (const i of r.instances) regionOf.set(i.name, r);

  const acc = new Map<string, { states: EdgeState[]; rtts: number[]; messages: number }>();
  const pairKey = (a: Region, b: Region) =>
    a.location < b.location ? `${a.location}|${b.location}` : `${b.location}|${a.location}`;

  for (const e of view.edges) {
    const a = regionOf.get(e.from);
    const b = regionOf.get(e.to);
    if (!a || !b || a === b) continue;
    const key = pairKey(a, b);
    const entry = acc.get(key) ?? { states: [], rtts: [], messages: 0 };
    entry.states.push(e.state);
    if (e.rttMs > 0 && e.state !== 'down') entry.rtts.push(e.rttMs);
    entry.messages += e.messages;
    acc.set(key, entry);
  }

  const links: Link[] = [];
  for (let i = 0; i < regions.length; i++) {
    for (let j = i + 1; j < regions.length; j++) {
      const a = regions[i];
      const b = regions[j];
      const key = pairKey(a, b);
      const entry = acc.get(key);
      const rtts = entry?.rtts ?? [];
      links.push({
        key,
        a,
        b,
        rttMs: rtts.length ? rtts.reduce((s, v) => s + v, 0) / rtts.length : 0,
        state: entry ? combineStates(entry.states) : 'pending',
        messages: entry?.messages ?? 0,
      });
    }
  }
  return links;
}

export interface ArcGeometry {
  d: string;
  start: [number, number];
  control: [number, number];
  end: [number, number];
}

/** A gentle upward-bowing curve between two map points. */
export function arcPath(a: Region, b: Region): ArcGeometry {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const dist = Math.hypot(dx, dy) || 1;
  let nx = -dy / dist;
  let ny = dx / dist;
  if (ny > 0) {
    nx = -nx;
    ny = -ny;
  }
  const bend = Math.min(dist * 0.24, 110);
  const cx = (a.x + b.x) / 2 + nx * bend;
  const cy = (a.y + b.y) / 2 + ny * bend;
  return {
    d: `M ${a.x.toFixed(1)} ${a.y.toFixed(1)} Q ${cx.toFixed(1)} ${cy.toFixed(1)} ${b.x.toFixed(1)} ${b.y.toFixed(1)}`,
    start: [a.x, a.y],
    control: [cx, cy],
    end: [b.x, b.y],
  };
}

/**
 * The colour each link state carries. Named rather than literal so the four
 * signals stay recognisable in either colour scheme: the light-mode values are
 * darker, because a tint that reads on midnight fjord vanishes on paper.
 *
 * These are CSS variable references, so they have to reach the DOM through a
 * `style` declaration. A presentation attribute such as `stroke="…"` does not
 * resolve `var()`.
 */
export const STATE_COLOR: Record<EdgeState, string> = {
  up: 'var(--status-up)',
  degraded: 'var(--status-degraded)',
  down: 'var(--status-down)',
  pending: 'var(--status-pending)',
};

export function formatUptime(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

export function formatMs(ms: number): string {
  if (ms <= 0) return '—';
  if (ms < 10) return `${ms.toFixed(1)} ms`;
  return `${Math.round(ms)} ms`;
}
