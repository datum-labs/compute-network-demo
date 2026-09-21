import { arcPath, formatMs } from './mesh';
import type { Link, Region } from './types';

/** Where a city's name sits relative to its pin. */
export type Side = 'right' | 'left' | 'below' | 'above';

export interface Box {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
}

/** An arc label sits at parameter t along its arc, nudged by offset pixels. */
export interface ArcLabelPlacement {
  t: number;
  offset: [number, number];
}

export interface LabelLayout {
  city: Map<string, Side>;
  arcs: Map<string, ArcLabelPlacement>;
  /**
   * Cities whose label could not be placed clearly. On small screens these
   * are dropped rather than left overlapping a pin or another name; the pin
   * still shows, and tapping it names the city.
   */
  hiddenCities: Set<string>;
  /**
   * Every box this layout reserved — pins, names and arc labels — so anything
   * drawn over the map afterwards can dodge what is already there.
   */
  boxes: Box[];
}

export type ToPx = (x: number, y: number) => [number, number];

/** Label sizes in pixels. Small screens get smaller type and tighter gaps. */
export interface LabelMetrics {
  /**
   * How much room a pin claims for itself. The count badge sits up and to the
   * right of the dot and reaches past the dot's own rings, so this is wider
   * than the pin looks.
   */
  pinClearance: number;
  gap: number;
  cityHeight: number;
  cityCharWidth: number;
  /**
   * Width per character of the location name under the city. It is set in
   * small, widely tracked capitals, and is the longer of the two lines:
   * "us-central-1" is four times the width of the "DFW" it replaced.
   */
  locationCharWidth: number;
  /** The line an expanded location adds: how many replicas, and how far apart. */
  detailCharWidth: number;
  detailHeight: number;
  arcHeight: number;
  arcCharWidth: number;
  /** Lift steps tried when an arc label does not fit on its arc. */
  lifts: number[];
}

export const FULL_METRICS: LabelMetrics = {
  pinClearance: 19,
  gap: 22,
  cityHeight: 40,
  cityCharWidth: 8.4,
  locationCharWidth: 8,
  detailCharWidth: 6.2,
  detailHeight: 17,
  arcHeight: 26,
  arcCharWidth: 6.7,
  lifts: [0, 30, 60, 90, 120, -30, -60, -90],
};

export const COMPACT_METRICS: LabelMetrics = {
  pinClearance: 17,
  gap: 19,
  cityHeight: 32,
  cityCharWidth: 7,
  locationCharWidth: 6.6,
  detailCharWidth: 5.4,
  detailHeight: 15,
  arcHeight: 22,
  arcCharWidth: 5.8,
  lifts: [0, 26, 52, -26, -52],
};

export const SELF_CHIP = { width: 104, height: 22, gap: 4 };
export const COMPACT_SELF_CHIP = { width: 86, height: 18, gap: 3 };

/**
 * What putting the visitor's badge beside its pin rather than under it costs,
 * in the same units as an overlap: about the area of one crowded name. Enough
 * that a clear side loses to a clear below, not so much that a badge is forced
 * on top of something else to avoid it.
 */
const SELF_ASIDE_PENALTY = 900;

/**
 * How close a card may come to something already drawn. A card whose edge is
 * flush against a pin still reads as sitting on it, because the pin's glow
 * reaches further than the box reserved for it.
 */
export const CARD_CLEARANCE = 14;

/**
 * What a pixel of distance from the fleet is worth against a pixel of map
 * covered. Small: a clear spot always beats a closer one that hides something.
 */
const NEARNESS = 0.25;

/** The same box with room around it. */
function grow(b: Box, by: number): Box {
  return { x0: b.x0 - by, y0: b.y0 - by, x1: b.x1 + by, y1: b.y1 + by };
}

function overlap(a: Box, b: Box): number {
  const w = Math.min(a.x1, b.x1) - Math.max(a.x0, b.x0);
  const h = Math.min(a.y1, b.y1) - Math.max(a.y0, b.y0);
  return w > 0 && h > 0 ? w * h : 0;
}

function cityBox(
  r: Region,
  side: Side,
  px: number,
  py: number,
  m: LabelMetrics,
  compact: boolean,
  detail?: string
): Box {
  const chip = compact ? COMPACT_SELF_CHIP : SELF_CHIP;
  const w =
    Math.max(
      r.city.length * m.cityCharWidth,
      r.location.length * m.locationCharWidth,
      (detail?.length ?? 0) * m.detailCharWidth,
      r.isSelf ? chip.width : 0
    ) + 6;
  const h = m.cityHeight + (detail ? m.detailHeight : 0) + (r.isSelf ? chip.height + chip.gap : 0);
  switch (side) {
    case 'right':
      return { x0: px + m.gap, y0: py - h / 2, x1: px + m.gap + w, y1: py + h / 2 };
    case 'left':
      return { x0: px - m.gap - w, y0: py - h / 2, x1: px - m.gap, y1: py + h / 2 };
    case 'below':
      return { x0: px - w / 2, y0: py + m.gap - 4, x1: px + w / 2, y1: py + m.gap - 4 + h };
    case 'above':
      return { x0: px - w / 2, y0: py - m.gap + 4 - h, x1: px + w / 2, y1: py - m.gap + 4 };
  }
}

export function arcLabelText(link: Link): string {
  if (link.state === 'down') return 'unreachable';
  if (link.state === 'pending') return 'connecting…';
  return formatMs(link.rttMs);
}

function arcLabelWidth(link: Link, m: LabelMetrics): number {
  return (link.a.city.length + link.b.city.length) * m.arcCharWidth + arcLabelText(link).length * 7 + 64;
}

export function pointOnArc(link: Link, t: number): [number, number] {
  const { start, control, end } = arcPath(link.a, link.b);
  const u = 1 - t;
  return [
    u * u * start[0] + 2 * u * t * control[0] + t * t * end[0],
    u * u * start[1] + 2 * u * t * control[1] + t * t * end[1],
  ];
}

/**
 * Greedy placement in screen pixels. Pins are reserved first so nothing
 * covers them, then city names, then arc labels. Each label takes the first
 * collision-free position, or the least-crowded one. Arc labels on short arcs
 * float away from the arc and are joined to it by a leader line.
 */
export function layoutLabels(
  regions: Region[],
  focusedLinks: Link[],
  toPx: ToPx,
  width: number,
  height: number,
  compact = false,
  focus: string | null = null,
  /** Screen pixels the replica fan takes up around each pin, when expanded. */
  spread = 0,
  /** Extra line under an expanded location's name, keyed by location. */
  details: Map<string, string> = new Map()
): LabelLayout {
  const base = compact ? COMPACT_METRICS : FULL_METRICS;
  // Expanded replicas surround the anchor, so names have to stand further off.
  const m: LabelMetrics = { ...base, pinClearance: base.pinClearance + spread, gap: base.gap + spread };
  const px = new Map(regions.map((r) => [r.location, toPx(r.x, r.y)]));
  const placed: Box[] = regions.map((r) => {
    const [x, y] = px.get(r.location)!;
    return { x0: x - m.pinClearance, y0: y - m.pinClearance, x1: x + m.pinClearance, y1: y + m.pinClearance };
  });
  const margin = 8;
  const offMap = (b: Box) =>
    (Math.max(0, margin - b.x0) +
      Math.max(0, b.x1 - width + margin) +
      Math.max(0, margin - b.y0) +
      Math.max(0, b.y1 - height + margin)) *
    60;
  const cost = (b: Box) => placed.reduce((sum, p) => sum + overlap(b, p), 0) + offMap(b);

  const city = new Map<string, Side>();
  const hiddenCities = new Set<string>();
  // The visitor's own city is placed first, then the spotlighted one, so the
  // two that matter most keep the clearest positions.
  const rank = (r: Region) => (r.isSelf ? 2 : r.location === focus ? 1 : 0);
  const ordered = [...regions].sort((a, b) => rank(b) - rank(a));
  for (const r of ordered) {
    const [x, y] = px.get(r.location)!;
    // A pin the camera has left behind still has a name; drawing it would put
    // stray words along the edge of the map.
    if (x < -m.pinClearance || x > width + m.pinClearance || y < -m.pinClearance || y > height + m.pinClearance) {
      hiddenCities.add(r.location);
      continue;
    }
    // The visitor's own city carries a wide badge above its name. Beside the
    // pin that badge rides level with the pin's own count and reads as a
    // collision, so the name goes under or over the pin unless both are taken.
    const sides: Side[] = r.isSelf ? ['below', 'above', 'right', 'left'] : ['right', 'left', 'below', 'above'];
    let best: { side: Side; box: Box; cost: number } | null = null;
    for (const side of sides) {
      const box = cityBox(r, side, x, y, m, compact, details.get(r.location));
      const c = cost(box) + (r.isSelf && (side === 'left' || side === 'right') ? SELF_ASIDE_PENALTY : 0);
      if (!best || c < best.cost) best = { side, box, cost: c };
      if (c === 0) break;
    }
    // A label that still overlaps this much would be unreadable.
    const crowded = compact && best!.cost > 220 && !r.isSelf && r.location !== focus;
    if (crowded) {
      hiddenCities.add(r.location);
      continue;
    }
    city.set(r.location, best!.side);
    placed.push(best!.box);
  }

  const arcs = new Map<string, ArcLabelPlacement>();
  const ts = [0.5, 0.4, 0.6, 0.3, 0.7];
  for (const link of [...focusedLinks].sort((a, b) => a.rttMs - b.rttMs)) {
    const w = arcLabelWidth(link, m);
    const h = m.arcHeight;
    // The arc bows upward, so lifting labels above it keeps them off the line.
    let best: { place: ArcLabelPlacement; box: Box; cost: number } | null = null;
    for (const lift of m.lifts) {
      for (const t of ts) {
        const [ax, ay] = pointOnArc(link, t);
        const [x, y0] = toPx(ax, ay);
        const y = y0 - lift;
        const box = { x0: x - w / 2, y0: y - h / 2, x1: x + w / 2, y1: y + h / 2 };
        const c = cost(box) + Math.abs(lift) * 2 + Math.abs(t - 0.5) * 40;
        if (!best || c < best.cost) best = { place: { t, offset: [0, -lift] }, box, cost: c };
      }
      if (best && best.cost - Math.abs(best.place.offset[1]) * 2 - 20 <= 0) break;
    }
    arcs.set(link.key, best!.place);
    placed.push(best!.box);
  }

  return { city, arcs, hiddenCities, boxes: placed };
}

/** Where a walkthrough card sits, and where its pointer leaves it. */
export interface CardPlacement {
  x: number;
  y: number;
  side: Side;
  /** The pointer's tip, relative to the card's top-left corner. */
  tail: [number, number];
  /**
   * Pixels of map the card would hide from where it landed. On a roomy screen
   * this is zero; on a cramped one every spot covers something, and the page
   * docks the card rather than letting it sit on the thing it describes.
   */
  covered: number;
}

/** How far a card stands off the thing it is pointing at, in pixels. */
const CARD_GAP = 26;

/**
 * Docks a card about the whole fleet into the clearest corner of the map.
 *
 * Such a card has nothing on screen to sit beside — the map is its subject —
 * and scoring it against the fleet's own box only ever chose between spots
 * that covered part of it, which is how a card ended up over open ocean with
 * a city underneath its edge. Corners are tried in reading order instead, so
 * an unobstructed card always lands in the same place rather than wherever the
 * arithmetic settled, and a corner that covers a pin or a name loses to one
 * that does not.
 */
export function placeFleetCard(
  bounds: Box,
  size: { width: number; height: number },
  reserved: Box[],
  margin = 16
): CardPlacement {
  const { width: w, height: h } = size;
  const left = bounds.x0 + margin;
  const right = Math.max(left, bounds.x1 - w - margin);
  const top = bounds.y0 + margin;
  const bottom = Math.max(top, bounds.y1 - h - margin);
  const midX = (left + right) / 2;
  const midY = (top + bottom) / 2;
  // Corners first, then the middle of each edge: a fleet across the middle of
  // a wide map leaves its room at the sides, and one across a tall map leaves
  // it above and below.
  const corners: { side: Side; x: number; y: number }[] = [
    { side: 'left', x: left, y: top },
    { side: 'left', x: left, y: bottom },
    { side: 'right', x: right, y: top },
    { side: 'right', x: right, y: bottom },
    { side: 'left', x: left, y: midY },
    { side: 'right', x: right, y: midY },
    { side: 'above', x: midX, y: top },
    { side: 'below', x: midX, y: bottom },
  ];

  // Among the spots that are clear, the one nearest the fleet: a card is a
  // caption on what is drawn, and one in the far corner of a big map reads as
  // a notice about the page instead.
  const fleetX = (bounds.x0 + bounds.x1) / 2;
  const fleetY = (bounds.y0 + bounds.y1) / 2;
  let best: { side: Side; x: number; y: number; cost: number; covered: number } | null = null;
  corners.forEach((corner, order) => {
    const box = grow({ x0: corner.x, y0: corner.y, x1: corner.x + w, y1: corner.y + h }, CARD_CLEARANCE);
    const covered = reserved.reduce((sum, r) => sum + overlap(box, r), 0);
    const away = Math.hypot(corner.x + w / 2 - fleetX, corner.y + h / 2 - fleetY);
    // Ties go to the corner listed first, which is the one that reads best.
    const cost = covered + away * NEARNESS + order;
    if (!best || cost < best.cost) best = { ...corner, cost, covered };
  });

  const { side, x, y } = best!;
  return { x, y, side, tail: [side === 'left' ? w : 0, h / 2], covered: best!.covered };
}

/**
 * Places a walkthrough card beside its subject without covering it.
 *
 * Same greedy scoring as the labels above: try each side, keep the cheapest,
 * where the cost is the area the card would hide plus what it would push off
 * the edge. The subject counts for far more than anything else — a card that
 * covers what it is pointing at explains nothing — and sides are tried in the
 * order that reads best, so an unobstructed card lands somewhere predictable
 * rather than wherever the arithmetic happened to tie.
 */
export function placeCard(
  subject: Box,
  size: { width: number; height: number },
  bounds: Box,
  reserved: Box[],
  margin = 12
): CardPlacement {
  const cx = (subject.x0 + subject.x1) / 2;
  const cy = (subject.y0 + subject.y1) / 2;
  const { width: w, height: h } = size;
  const fit = (v: number, span: number, lo: number, hi: number) =>
    Math.min(Math.max(v, lo + margin), Math.max(lo + margin, hi - span - margin));

  const spots: { side: Side; x: number; y: number }[] = [];
  for (const anchor of [cy - h / 2, subject.y0 - h / 4, subject.y1 - h + h / 4]) {
    spots.push({ side: 'right', x: subject.x1 + CARD_GAP, y: anchor });
    spots.push({ side: 'left', x: subject.x0 - CARD_GAP - w, y: anchor });
  }
  for (const anchor of [cx - w / 2, subject.x0 - w / 4, subject.x1 - w + w / 4]) {
    spots.push({ side: 'below', x: anchor, y: subject.y1 + CARD_GAP });
    spots.push({ side: 'above', x: anchor, y: subject.y0 - CARD_GAP - h });
  }

  let best: { side: Side; x: number; y: number; cost: number; covered: number } | null = null;
  for (let order = 0; order < spots.length; order++) {
    const spot = spots[order];
    const x = fit(spot.x, w, bounds.x0, bounds.x1);
    const y = fit(spot.y, h, bounds.y0, bounds.y1);
    const box = grow({ x0: x, y0: y, x1: x + w, y1: y + h }, CARD_CLEARANCE);
    const covered = overlap(box, subject) + reserved.reduce((sum, r) => sum + overlap(box, r), 0);
    const cost =
      // A card over its own subject explains nothing, so that counts for far
      // more than covering anything else.
      overlap(box, subject) * 11 +
      covered +
      // Ties go to the side listed first, which is the one that reads best.
      order;
    if (!best || cost < best.cost) best = { side: spot.side, x, y, cost, covered };
  }

  const { side, x, y } = best!;
  const edge = (v: number, lo: number, hi: number) => Math.min(Math.max(v, lo), Math.max(lo, hi));
  const tail: [number, number] =
    side === 'right'
      ? [0, edge(cy - y, 22, h - 22)]
      : side === 'left'
        ? [w, edge(cy - y, 22, h - 22)]
        : side === 'below'
          ? [edge(cx - x, 26, w - 26), 0]
          : [edge(cx - x, 26, w - 26), h];
  return { x, y, side, tail, covered: best!.covered };
}
