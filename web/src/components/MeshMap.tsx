import { AnimatePresence, motion } from 'motion/react';
import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { type Camera, ZOOM_STEP, fitCamera, useMapCamera } from '../camera';
import { type Box, COMPACT_SELF_CHIP, SELF_CHIP, type Side, type ToPx, arcLabelText, layoutLabels, pointOnArc } from '../labels';
import { MAP_HEIGHT, MAP_WIDTH, STATE_COLOR, arcPath, fanOffsets, formatMs } from '../mesh';
import type { Stage, StoryShot } from '../story';
import type { IntraLink, Link, Region, Replica } from '../types';
import { InstanceCard } from './InstanceCard';
import { cardFitsOnMap } from './StoryCard';

const EASE_OUT = [0.22, 1, 0.36, 1] as const;

/**
 * How far in, as a multiple of the whole-fleet framing, a location stops being
 * one dot and becomes its replicas. Collapsing and expanding follows the zoom
 * rather than a switch, so there is one thing to learn: get closer to see more.
 * It is a multiple rather than an absolute scale because the whole-fleet
 * framing is a different scale on a projector than on a phone.
 */
const EXPAND_FROM = 1.6;
const EXPAND_TO = 2.6;

/** How far the replicas of one location sit from its anchor, in screen pixels. */
const FAN_RADIUS = 56;
const COMPACT_FAN_RADIUS = 38;

/** The camera goes back to the whole fleet this long after the last gesture. */
const IDLE_RESET_MS = 18000;

interface Props {
  width: number;
  height: number;
  /** Small screens: smaller labels, one edge label at a time, less motion. */
  compact?: boolean;
  reducedMotion?: boolean;
  regions: Region[];
  links: Link[];
  /** Traffic between replicas sharing a location, drawn once it is expanded. */
  intraLinks: IntraLink[];
  focus: string | null;
  /** Links whose arc should be lit because a message just landed in the feed. */
  flashing?: Set<string>;
  selected: string | null;
  receivedAt: number;
  onHover: (location: string | null) => void;
  onSelect: (location: string | null) => void;
  /** Tells the page the visitor is steering, so the spotlight tour stands by. */
  onEngage?: (engaged: boolean) => void;
  /** Where the walkthrough wants the camera; null leaves the visitor in charge. */
  shot?: StoryShot | null;
  /** The walkthrough cutting rather than flying, for reduced motion. */
  cut?: boolean;
  /**
   * Dims the map around whatever the walkthrough is talking about. A location
   * gets a clearing around its Instances; null dims the map evenly, for a card
   * whose subject is off the map altogether.
   */
  spotlight?: { location: string | null } | null;
  /**
   * Reports whether a walkthrough card could float here clear of the fleet.
   * Asked before the walkthrough opens itself, so it is reported whether or
   * not a card is showing.
   */
  onRoom?: (roomy: boolean) => void;
  /** Reports where things are, so a card over the map can avoid covering them. */
  onStage?: (stage: Stage) => void;
}

function projector(cam: Camera, width: number): ToPx {
  const s = width / cam.w;
  return (x, y) => [(x - cam.x) * s, (y - cam.y) * s];
}

const clamp01 = (v: number) => Math.min(1, Math.max(0, v));

/** How far out of the anchor the replicas have sprung, from 0 to 1. */
function expansionAt(zoomMultiple: number): number {
  return clamp01((zoomMultiple - EXPAND_FROM) / (EXPAND_TO - EXPAND_FROM));
}

/**
 * The map is memoised because the page re-renders several times a second for
 * the traffic feed, and none of those renders have anything to tell it.
 */
export const MeshMap = memo(MeshMapImpl);

function MeshMapImpl({
  width,
  height,
  compact = false,
  reducedMotion = false,
  regions,
  links,
  intraLinks,
  focus,
  flashing,
  selected,
  receivedAt,
  onHover,
  onSelect,
  onEngage,
  shot = null,
  cut = false,
  spotlight = null,
  onRoom,
  onStage,
}: Props) {
  const aspect = width / height;
  // Re-frame only when the set of places or the shape of the map changes,
  // not on every poll.
  const placesKey = regions.map((r) => r.location).join(',');
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const fit = useMemo(() => fitCamera(regions, aspect, width, compact), [placesKey, aspect, width, compact]);
  // While the story is driving, the idle reset stands down: a chapter can hold
  // one location for longer than an abandoned page would be left alone.
  const camera = useMapCamera(fit, width, height, shot ? 0 : IDLE_RESET_MS);
  const { cam, target, engaged } = camera;
  const toPx = useCallback<ToPx>((x, y) => projector(cam, width)(x, y), [cam, width]);
  // Map units per screen pixel; keeps pins and strokes a constant size on
  // screen whatever the zoom.
  const unit = cam.w / width;
  // Pins stay large enough to tap: the hit circle is 18 units, so 1.25 gives
  // a target of about 45px across.
  const pinScale = unit * (compact ? 1.25 : 1.45);
  const fanRadius = compact ? COMPACT_FAN_RADIUS : FAN_RADIUS;

  // Drawing follows the camera as it flies; labels are laid out against where
  // it is heading, so they do not reshuffle on the way.
  const expansion = expansionAt(fit.w / cam.w);
  // Past the point where the replicas are fully out the world recedes further
  // still. The subject by then is the Instances, and the map's dots are so
  // magnified that leaving them up would be noise rather than ground.
  const deep = clamp01((fit.w / cam.w - EXPAND_TO) / 4);
  const settledExpansion = expansionAt(camera.zoom);
  // Past the point where the replicas are fully out, keep pushing them apart:
  // zooming further should go on revealing rather than only magnifying the
  // ground under them.
  const spread = (multiple: number) => Math.min(compact ? 1.5 : 2.4, Math.max(1, multiple / EXPAND_TO));
  const radius = fanRadius * spread(fit.w / cam.w);
  // Names stand off by whatever the fan takes up, and are laid out to match.
  const labelSpread = fanRadius * spread(camera.zoom) * settledExpansion;
  // Clear the replicas' own names too, which sit just under their pins; with
  // five in one location there is a replica on every side of the anchor.
  const labelOffset = (compact ? 14 : 18) + labelSpread + 18 * settledExpansion;

  useEffect(() => onEngage?.(engaged), [engaged, onEngage]);

  // Story mode steers through the same controls a visitor uses, so there is
  // one way into a framing rather than two that can disagree.
  const regionsRef = useRef(regions);
  regionsRef.current = regions;
  const shotKey = shot ? (shot.at === 'fleet' ? 'fleet' : `${shot.location}@${shot.closeness}`) : null;
  useEffect(() => {
    if (!shotKey || !shot) return;
    if (shot.at === 'fleet') {
      camera.reset(!cut);
      return;
    }
    const region = regionsRef.current.find((r) => r.location === shot.location);
    if (region) camera.focusRegion(region, shot.closeness, !cut);
    // The shot is identified by its key; rebuilding it every poll is not a cue
    // to fly again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shotKey, cut, placesKey, camera.reset, camera.focusRegion]);

  const replicas = useMemo(
    () => layOutReplicas(regions, unit, radius, expansion),
    [regions, unit, radius, expansion]
  );
  const positions = useMemo(() => new Map(replicas.map((r) => [r.instance.name, r])), [replicas]);

  const card = regions.find((r) => r.location === selected) ?? null;
  // An open card already describes its city; arc labels would sit under it. On
  // a small screen an expanded location has no room for them either, and by
  // then the subject is the instances rather than the distance between cities.
  const quiet = card !== null || (compact && settledExpansion > 0.4);
  const focusedLinks = useMemo(
    () => (quiet ? [] : links.filter((l) => focus && (l.a.location === focus || l.b.location === focus))),
    [links, focus, quiet]
  );
  // A phone has no room for five labels at once, so they take turns.
  const labelTurn = useSpotlight(compact && !reducedMotion ? focusedLinks.length : 0);
  const labelledLinks = useMemo(() => {
    if (!compact) return focusedLinks;
    const ordered = [...focusedLinks].sort((a, b) => a.rttMs - b.rttMs);
    return ordered.length ? [ordered[labelTurn % ordered.length]] : [];
  }, [compact, focusedLinks, labelTurn]);

  // Expanded locations say what their replicas cost each other to reach, which
  // is the point of showing them at all.
  const details = useMemo(
    () => (settledExpansion > 0.6 ? intraDetails(regions, intraLinks, compact) : new Map<string, string>()),
    [regions, intraLinks, compact, settledExpansion]
  );
  const layout = useMemo(
    () =>
      layoutLabels(
        regions,
        labelledLinks,
        projector(target, width),
        width,
        height,
        compact,
        focus,
        labelSpread,
        details
      ),
    [regions, labelledLinks, target, width, height, compact, focus, labelSpread, details]
  );

  // What each pin and the fleet as a whole take up on the map, in the map's
  // own pixels: rings and hit target whatever the zoom, plus the Instances
  // fanned around them and the badge above them. Laid out against where the
  // camera is heading, like the labels, so a card placed from this does not
  // shuffle about while the map is still flying.
  const drawn = useMemo(() => {
    const at = projector(target, width);
    const clearance = (compact ? 34 : 48) + labelSpread;
    const crown = compact ? 14 : 20;
    const around = ([x, y]: [number, number]): Box => ({
      x0: x - clearance,
      y0: y - clearance - crown,
      x1: x + clearance,
      y1: y + clearance,
    });
    const points = regions.map((r) => at(r.x, r.y));
    const subjects: Record<string, Box> = {};
    regions.forEach((r, i) => {
      subjects[r.location] = around(points[i]);
    });
    const fleet: Box = points.length
      ? {
          x0: Math.min(...points.map((p) => p[0])) - clearance,
          y0: Math.min(...points.map((p) => p[1])) - clearance - crown,
          x1: Math.max(...points.map((p) => p[0])) + clearance,
          y1: Math.max(...points.map((p) => p[1])) + clearance,
        }
      : { x0: 0, y0: 0, x1: width, y1: height };
    return { subjects, fleet };
  }, [regions, target, width, height, compact, labelSpread]);

  // Whether a walkthrough card could float here clear of the fleet. The page
  // asks before opening the walkthrough by itself. Nothing is said until there
  // is a fleet: a map with no pins on it yet has no answer, and "no room" is
  // not the same as "not yet".
  const roomy = regions.length > 0 ? cardFitsOnMap(drawn.fleet, { width, height }, compact) : null;
  useEffect(() => {
    if (roomy !== null) onRoom?.(roomy);
  }, [roomy, onRoom]);

  useEffect(() => {
    const el = camera.surface.current;
    if (!onStage || !el) return;
    const publish = () => {
      const box = el.getBoundingClientRect();
      const shift = (b: Box): Box => ({
        x0: b.x0 + box.left,
        y0: b.y0 + box.top,
        x1: b.x1 + box.left,
        y1: b.y1 + box.top,
      });
      const subjects: Record<string, Box> = {};
      for (const [location, b] of Object.entries(drawn.subjects)) subjects[location] = shift(b);
      onStage({
        surface: { x0: box.left, y0: box.top, x1: box.right, y1: box.bottom },
        reserved: layout.boxes.map(shift),
        subjects,
        fleet: shift(drawn.fleet),
      });
    };
    publish();
    // A stacked layout scrolls under a pinned map, so the surface moves.
    window.addEventListener('scroll', publish, { passive: true, capture: true });
    window.addEventListener('resize', publish);
    return () => {
      window.removeEventListener('scroll', publish, { capture: true });
      window.removeEventListener('resize', publish);
    };
  }, [onStage, layout, drawn, camera.surface]);

  // The clearing follows the camera as it flies, so the subject stays lit the
  // whole way in rather than arriving already framed.
  const lit = spotlight?.location ? regions.find((r) => r.location === spotlight.location) : null;
  const clearing = lit ? toPx(lit.x, lit.y) : null;

  return (
    <div
      ref={camera.surface}
      className="relative"
      style={{ width, height, touchAction: engaged ? 'none' : 'pan-y', cursor: engaged ? 'grab' : 'default' }}
      onClick={() => {
        if (camera.dragged()) return;
        onSelect(null);
      }}
    >
      <svg viewBox={`${cam.x} ${cam.y} ${cam.w} ${cam.h}`} className="map-canvas absolute inset-0 h-full w-full">
        <defs>
          {/* The glow around a pin, as a gradient rather than a blur: it is the
              same soft disc, and a filter would be re-rasterised every time the
              camera moved under it. */}
          <radialGradient id="halo-live">
            <stop offset="0%" stopColor="#B3D56F" stopOpacity={1} />
            <stop offset="42%" stopColor="#B3D56F" stopOpacity={0.88} />
            <stop offset="68%" stopColor="#B3D56F" stopOpacity={0.36} />
            <stop offset="100%" stopColor="#B3D56F" stopOpacity={0} />
          </radialGradient>
          <radialGradient id="halo-starting">
            <stop offset="0%" stopColor="#F2C46D" stopOpacity={1} />
            <stop offset="42%" stopColor="#F2C46D" stopOpacity={0.88} />
            <stop offset="68%" stopColor="#F2C46D" stopOpacity={0.36} />
            <stop offset="100%" stopColor="#F2C46D" stopOpacity={0} />
          </radialGradient>
          <radialGradient id="pin-core" cx="35%" cy="35%" r="70%">
            <stop offset="0%" stopColor="#F4FBD2" />
            <stop offset="55%" stopColor="#B3D56F" />
            <stop offset="100%" stopColor="#7FA64A" />
          </radialGradient>
        </defs>

        {/* The dots coarsen as the camera closes in, so the map recedes to a
            texture once the subject is the instances rather than the world. */}
        <image
          href="/world-map-dots.svg"
          x={0}
          y={0}
          width={MAP_WIDTH}
          height={MAP_HEIGHT}
          className="map-dots"
          opacity={Math.max(0.07, 1 - 0.65 * expansion - 0.28 * deep)}
        />

        {/* One arc per pair of locations, anchored at the location rather than
            at each replica: fifty-odd strands between eight instances would be
            a thicket, and the story is region to region. */}
        <g>
          <AnimatePresence>
            {links.map((link, i) => (
              <Arc
                key={link.key}
                link={link}
                index={i}
                unit={unit}
                flashing={flashing?.has(link.key) ?? false}
                compact={compact}
                reducedMotion={reducedMotion}
                emphasis={focus ? (link.a.location === focus || link.b.location === focus ? 'high' : 'low') : 'normal'}
              />
            ))}
          </AnimatePresence>
        </g>

        {expansion > 0.01 && (
          <g opacity={expansion}>
            {intraLinks.map((link) => {
              const a = positions.get(link.from);
              const b = positions.get(link.to);
              if (!a || !b) return null;
              return (
                <line
                  key={link.key}
                  x1={a.x}
                  y1={a.y}
                  x2={b.x}
                  y2={b.y}
                  stroke={link.draining ? STATE_COLOR.pending : STATE_COLOR[link.state]}
                  strokeWidth={(flashing?.has(link.key) ? 2.2 : 1.3) * unit}
                  // A link to a draining Instance thins to a dashed trace, so
                  // the traffic is visibly winding down before it goes.
                  strokeDasharray={link.draining ? `${2 * unit} ${4 * unit}` : undefined}
                  strokeOpacity={link.draining ? 0.3 : flashing?.has(link.key) ? 0.95 : 0.5}
                  strokeLinecap="round"
                />
              );
            })}
          </g>
        )}

        <g>
          <AnimatePresence>
            {regions.map((region) => (
              <Pin
                key={region.location}
                region={region}
                scale={pinScale}
                compact={compact}
                reducedMotion={reducedMotion}
                expansion={region.instances.length > 1 ? expansion : 0}
                focused={region.location === focus}
                onHover={onHover}
                onSelect={(location) => {
                  onSelect(location);
                  camera.focusRegion(region);
                }}
              />
            ))}
          </AnimatePresence>
        </g>

        <g>
          <AnimatePresence>
            {expansion > 0.01 &&
              replicas.map((replica) => (
                <ReplicaPin
                  key={replica.instance.name}
                  replica={replica}
                  scale={pinScale}
                  expansion={expansion}
                  reducedMotion={reducedMotion}
                  onSelect={(location) => {
                    onSelect(location);
                    camera.focusRegion(replica.region);
                  }}
                />
              ))}
          </AnimatePresence>
        </g>
      </svg>

      {/* Crisp HTML typography layered over the map. */}
      <div className="pointer-events-none absolute inset-0">
        <svg className="absolute inset-0 h-full w-full overflow-visible">
          <AnimatePresence>
            {labelledLinks.map((link) => {
              const place = layout.arcs.get(link.key);
              if (!place || place.offset[1] === 0) return null;
              const [x, y] = toPx(...pointOnArc(link, place.t));
              return (
                <motion.line
                  key={`${focus}:${link.key}`}
                  x1={x}
                  y1={y}
                  x2={x}
                  y2={y + place.offset[1] + 12}
                  stroke="#E6F59E"
                  strokeOpacity={0.35}
                  strokeWidth={1}
                  strokeDasharray="2 3"
                  initial={{ opacity: 0 }}
                  animate={{ opacity: 1 }}
                  exit={{ opacity: 0 }}
                />
              );
            })}
          </AnimatePresence>
        </svg>

        <AnimatePresence>
          {regions.map((region) =>
            // The open card names the location it points at, so the label
            // under it would only be something for the card to cover.
            layout.hiddenCities.has(region.location) || region === card ? null : (
              <CityLabel
                key={region.location}
                region={region}
                compact={compact}
                at={toPx(region.x, region.y)}
                side={layout.city.get(region.location) ?? 'right'}
                offset={labelOffset}
                detail={details.get(region.location)}
                focused={region.location === focus}
              />
            )
          )}
        </AnimatePresence>

        {expansion > 0.55 &&
          replicas.map((replica) => {
            const at = toPx(replica.x, replica.y);
            // Only for replicas the camera is actually showing.
            if (at[0] < 0 || at[0] > width || at[1] < 0 || at[1] > height) return null;
            return (
              <ReplicaLabel
                key={replica.instance.name}
                replica={replica}
                compact={compact}
                at={at}
                opacity={clamp01((expansion - 0.55) / 0.35)}
              />
            );
          })}

        <AnimatePresence>
          {labelledLinks.map((link) => {
            const place = layout.arcs.get(link.key) ?? { t: 0.5, offset: [0, 0] };
            const [x, y] = toPx(...pointOnArc(link, place.t));
            // Keep the label fully on the map, whatever the arc does.
            const halfWidth = Math.min(width / 2 - 6, 90 + link.a.city.length * 3.4 + link.b.city.length * 3.4);
            const cx = Math.min(Math.max(x + place.offset[0], halfWidth + 6), width - halfWidth - 6);
            const cy = Math.min(Math.max(y + place.offset[1], 18), height - 18);
            return <ArcLabel key={`${focus}:${link.key}`} link={link} focus={focus!} at={[cx, cy]} />;
          })}
        </AnimatePresence>
      </div>

      <AnimatePresence>
        {spotlight && (
          <motion.div
            key="spotlight"
            className="pointer-events-none absolute inset-0"
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: reducedMotion ? 0 : 0.7, ease: EASE_OUT }}
            style={{
              background: clearing
                ? `radial-gradient(circle ${Math.round(Math.max(compact ? 110 : 150, radius * expansion + 110))}px at ${Math.round(clearing[0])}px ${Math.round(clearing[1])}px, rgba(6,16,28,0) 0%, rgba(6,16,28,0) 58%, rgba(6,16,28,0.58) 100%)`
                : 'rgba(6,16,28,0.42)',
            }}
          />
        )}
      </AnimatePresence>

      {/* The way back to the whole fleet is the visitor's, not the story's:
          offering it while the story is flying would read as their doing. */}
      <ZoomControls camera={camera} compact={compact} steerable={!shot} />

      {/* On a small screen the card is rendered below the map instead, where
          it does not cover the thing it describes. */}
      <AnimatePresence>
        {card && !compact && (
          <InstanceCard
            key={card.location}
            region={card}
            at={toPx(card.x, card.y)}
            // Clear the replicas fanned around the pin the card describes.
            clearance={28 + radius * expansion}
            mapWidth={width}
            mapHeight={height}
            receivedAt={receivedAt}
            onClose={() => onSelect(null)}
          />
        )}
      </AnimatePresence>
    </div>
  );
}

/** Fans each location's replicas around its anchor, in map units. */
function layOutReplicas(regions: Region[], unit: number, radiusPx: number, expansion: number): Replica[] {
  const out: Replica[] = [];
  for (const region of regions) {
    if (region.instances.length < 2) continue;
    const offsets = fanOffsets(region.instances.length, radiusPx * expansion * unit);
    region.instances.forEach((instance, i) => {
      out.push({
        instance,
        region,
        ordinal: i + 1,
        x: region.x + offsets[i][0],
        y: region.y + offsets[i][1],
      });
    });
  }
  return out;
}

/** The line an expanded location adds under its name. */
function intraDetails(regions: Region[], intraLinks: IntraLink[], compact: boolean): Map<string, string> {
  const rtts = new Map<string, number[]>();
  for (const link of intraLinks) {
    if (link.rttMs <= 0) continue;
    rtts.set(link.location, [...(rtts.get(link.location) ?? []), link.rttMs]);
  }
  const details = new Map<string, string>();
  for (const region of regions) {
    // What the location is scaled to, which is not the same as what is still
    // on the map while an Instance drains.
    const count = region.instances.filter((i) => i.status !== 'stopping').length;
    if (count < 2) continue;
    const measured = rtts.get(region.location) ?? [];
    const avg = measured.length ? measured.reduce((s, v) => s + v, 0) / measured.length : 0;
    const apart = avg > 0 ? formatMs(avg) : '—';
    details.set(region.location, compact ? `${count} Instances · ${apart}` : `${count} Instances · ${apart} apart`);
  }
  return details;
}

function Arc({
  link,
  index,
  unit,
  flashing,
  compact,
  reducedMotion,
  emphasis,
}: {
  link: Link;
  index: number;
  unit: number;
  flashing: boolean;
  compact: boolean;
  reducedMotion: boolean;
  emphasis: 'high' | 'normal' | 'low';
}) {
  const { d } = arcPath(link.a, link.b);
  const color = STATE_COLOR[link.state];
  // Streaks are the most expensive thing on screen: one per arc on a phone,
  // and none at all when reduced motion is asked for.
  const flowing = !reducedMotion && (link.state === 'up' || link.state === 'degraded');
  const solid = link.state === 'up';
  // A message landing in the feed lights its arc, tying the two together.
  const baseOpacity = flashing ? 0.95 : { high: 0.8, normal: 0.36, low: 0.17 }[emphasis];
  const width = (flashing ? 2 : emphasis === 'high' ? 1.6 : 1.1) * unit;
  // Longer round trips pulse more slowly, so latency is visible at a glance.
  const duration = Math.min(5.5, Math.max(1.6, 1.3 + link.rttMs / 45));
  const delay = 1.1 + ((index * 0.37) % 1.8);

  return (
    <motion.g initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0, transition: { duration: 0.6 } }}>
      {solid ? (
        <motion.path
          d={d}
          fill="none"
          stroke={color}
          strokeWidth={width}
          strokeLinecap="round"
          initial={{ pathLength: 0, strokeOpacity: baseOpacity }}
          animate={{ pathLength: 1, strokeOpacity: baseOpacity, strokeWidth: width }}
          transition={{
            pathLength: { duration: 1.4, ease: EASE_OUT },
            strokeOpacity: { duration: flashing ? 0.12 : 0.55 },
            strokeWidth: { duration: flashing ? 0.12 : 0.55 },
          }}
        />
      ) : (
        <motion.path
          d={d}
          fill="none"
          stroke={color}
          strokeWidth={width}
          strokeDasharray={link.state === 'pending' ? `${1.5 * unit} ${5 * unit}` : `${6 * unit} ${5 * unit}`}
          strokeLinecap="round"
          initial={{ strokeOpacity: 0 }}
          animate={{
            strokeOpacity: link.state === 'pending' ? Math.max(baseOpacity, 0.3) : Math.max(baseOpacity, 0.7),
          }}
          transition={{ duration: 0.8, ease: EASE_OUT }}
        />
      )}
      {/* The streaks used to carry a Gaussian blur for bloom. Anything drawn
          along a dash that moves is re-rasterised across the whole arc on
          every frame, and at full screen that bloom alone was most of the
          demo's frame budget — as a filter or as a second, wider stroke. The
          dash is drawn a little heavier instead. */}
      {flowing && (
        <g opacity={emphasis === 'low' ? 0.45 : 1}>
          <path
            d={d}
            pathLength={100}
            className="flow flow-forward"
            stroke={solid ? '#E6F59E' : color}
            strokeWidth={2.8 * unit}
            style={{ animationDuration: `${duration}s`, animationDelay: `${delay}s` }}
          />
          {!compact && (
            <path
              d={d}
              pathLength={100}
              className="flow flow-reverse"
              stroke={solid ? '#E6F59E' : color}
              strokeWidth={2.8 * unit}
              style={{ animationDuration: `${duration * 1.07}s`, animationDelay: `${delay + duration / 2}s` }}
            />
          )}
        </g>
      )}
    </motion.g>
  );
}

interface PinProps {
  region: Region;
  scale: number;
  compact: boolean;
  reducedMotion: boolean;
  /** How far this location's replicas have sprung out of the dot, from 0 to 1. */
  expansion: number;
  focused: boolean;
  onHover: (location: string | null) => void;
  onSelect: (location: string | null) => void;
}

function Pin({ region, scale, compact, reducedMotion, expansion, focused, onHover, onSelect }: PinProps) {
  const running = region.instances.some((i) => i.status === 'running');
  // Rings on every pin are wasted work on a phone; the eye follows the
  // spotlight and the visitor's own city anyway.
  const pulsing = running && !reducedMotion && (!compact || focused || region.isSelf);
  // A collapsed location says it is changing through its badge; the ring is
  // what makes the change worth looking up for.
  const arriving = !reducedMotion && region.instances.some((i) => i.status === 'starting');
  // Instances on their way out are not part of what the location is scaled to.
  const count = region.instances.filter((i) => i.status !== 'stopping').length;
  // Once the replicas are out, the dot stays as the hub their arcs run from.
  const hub = expansion > 0.01;
  return (
    <g data-pin data-location={region.location} transform={`translate(${region.x.toFixed(2)} ${region.y.toFixed(2)}) scale(${scale.toFixed(4)})`}>
      <motion.g
        initial={{ opacity: 0, y: -46, scale: 0.3 }}
        animate={{
          opacity: 1 - 0.55 * expansion,
          y: 0,
          scale: (focused ? 1.18 : 1) * (1 - 0.35 * expansion),
        }}
        exit={{ opacity: 0, scale: 0.4, transition: { duration: 0.7, ease: EASE_OUT } }}
        transition={{ type: 'spring', stiffness: 170, damping: 16, mass: 0.9 }}
        style={{ cursor: 'pointer' }}
        onMouseEnter={() => onHover(region.location)}
        onMouseLeave={() => onHover(null)}
        onClick={(e) => {
          e.stopPropagation();
          onSelect(region.location);
        }}
      >
        {/* Landing ripple, played once. */}
        <motion.circle
          r={6}
          fill="none"
          stroke="#E6F59E"
          strokeWidth={1.2}
          initial={{ scale: 1, opacity: 0.9 }}
          animate={{ scale: 5, opacity: 0 }}
          transition={{ duration: 1.6, ease: EASE_OUT, delay: 0.25 }}
        />
        <circle r={26} fill="url(#halo-live)" opacity={region.isSelf ? 0.34 : 0.22} />
        {pulsing && !hub && (
          <>
            <circle r={6} className="pulse-ring" />
            {!compact && <circle r={6} className="pulse-ring pulse-ring-late" />}
          </>
        )}
        {arriving && !hub && <circle r={7.5} className="arrive-ring" />}
        {region.isSelf && !reducedMotion && !hub && <circle r={12.5} className="self-ring" />}
        <circle r={5.6} fill={running ? 'url(#pin-core)' : '#6B7F93'} />
        <circle r={2} fill="#0c1d31" opacity={0.85} />
        {count > 1 && !hub && (
          <g transform="translate(7 -7)">
            {/* Remounting on every change replays the spring, so a location
                scaling out ticks up where the eye can catch it. */}
            <motion.g
              key={count}
              initial={reducedMotion ? false : { scale: 0.3, opacity: 0 }}
              animate={{ scale: 1, opacity: 1 }}
              transition={{ type: 'spring', stiffness: 420, damping: 17 }}
              style={{ transformBox: 'fill-box', transformOrigin: 'center' }}
            >
              <circle r={5.2} fill="#E6F59E" />
              <text textAnchor="middle" dy="2.4" fontSize="7" fontWeight={600} fill="#0c1d31">
                {count}
              </text>
            </motion.g>
          </g>
        )}
        <circle r={18} fill="transparent" />
      </motion.g>
    </g>
  );
}

/** One replica, sprung out of its location's dot. */
function ReplicaPin({
  replica,
  scale,
  expansion,
  reducedMotion,
  onSelect,
}: {
  replica: Replica;
  scale: number;
  expansion: number;
  reducedMotion: boolean;
  onSelect: (location: string) => void;
}) {
  const { instance, region } = replica;
  const starting = instance.status === 'starting';
  const stopping = instance.status === 'stopping';
  const core = starting ? '#F2C46D' : stopping ? '#6B7F93' : 'url(#pin-core)';
  return (
    <motion.g
      data-pin
      data-replica
      data-location={region.location}
      initial={{ x: region.x, y: region.y, opacity: 0 }}
      // A draining Instance fades back rather than vanishing when it goes.
      animate={{ x: replica.x, y: replica.y, opacity: expansion * (stopping ? 0.55 : 1) }}
      exit={{ x: region.x, y: region.y, opacity: 0, transition: { duration: 0.35, ease: EASE_OUT } }}
      transition={
        // Springing out of the dot is the point of the gesture, but a visitor
        // who asked for less motion gets the result without the flourish.
        reducedMotion
          ? { duration: 0.12 }
          : { type: 'spring', stiffness: 260, damping: 24, mass: 0.6, opacity: { duration: 0.25 } }
      }
      style={{ cursor: 'pointer' }}
      onClick={(e) => {
        e.stopPropagation();
        onSelect(region.location);
      }}
    >
      <g transform={`scale(${scale.toFixed(4)})`}>
        <circle
          r={23}
          fill={starting ? 'url(#halo-starting)' : 'url(#halo-live)'}
          opacity={instance.isSelf ? 0.3 : 0.16}
        />
        {!reducedMotion && starting && <circle r={6.5} className="arrive-ring" />}
        {!reducedMotion && stopping && <circle r={6} className="drain-ring" />}
        {instance.isSelf && !reducedMotion && <circle r={10} className="self-ring" />}
        <circle r={4.6} fill={core} />
        <circle r={1.6} fill="#0c1d31" opacity={0.85} />
        <circle r={14} fill="transparent" />
      </g>
    </motion.g>
  );
}

/** Names a replica once its location is open far enough to read them. */
function ReplicaLabel({
  replica,
  at,
  compact,
  opacity,
}: {
  replica: Replica;
  at: [number, number];
  compact: boolean;
  opacity: number;
}) {
  return (
    // Anchored by transform rather than by left and top: the camera moves
    // these on every frame of a flight, and only a transform stays off layout.
    <div className="absolute top-0 left-0" style={{ transform: `translate3d(${at[0]}px, ${at[1]}px, 0)`, opacity }}>
      <div
        className={`absolute -translate-x-1/2 translate-y-[13px] whitespace-nowrap ${
          compact ? 'text-[9.5px]' : 'text-[11px]'
        } font-medium tracking-[0.04em] ${
          replica.instance.isSelf ? 'text-[#E6F59E]' : 'text-white/65'
        } [text-shadow:0_1px_8px_rgba(12,29,49,0.95)]`}
      >
        Instance {replica.ordinal}
        {replica.instance.status === 'starting' && <span className="text-[#F2C46D]"> · starting</span>}
        {replica.instance.status === 'stopping' && <span className="text-white/40"> · draining</span>}
      </div>
    </div>
  );
}

const SIDE_CLASS: Record<Side, string> = {
  right: 'top-0 -translate-y-1/2 items-start',
  left: 'top-0 -translate-y-1/2 items-end',
  below: 'left-0 -translate-x-1/2 items-center',
  above: 'left-0 -translate-x-1/2 items-center',
};

/** Where the name sits relative to the pin, matching what was laid out. */
function sideOffset(side: Side, offset: number): React.CSSProperties {
  switch (side) {
    case 'right':
      return { left: offset };
    case 'left':
      return { right: offset };
    case 'below':
      return { top: offset };
    case 'above':
      return { bottom: offset };
  }
}

function CityLabel({
  region,
  at,
  side,
  offset,
  compact,
  detail,
  focused,
}: {
  region: Region;
  at: [number, number];
  side: Side;
  /** How far off the pin the name stands, clearing any replicas around it. */
  offset: number;
  compact: boolean;
  /** Shown while the location is expanded: how many replicas, and how far apart. */
  detail?: string;
  focused: boolean;
}) {
  const chip = compact ? COMPACT_SELF_CHIP : SELF_CHIP;
  return (
    <motion.div
      className="absolute top-0 left-0"
      style={{ x: at[0], y: at[1] }}
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.6, delay: 0.35, ease: EASE_OUT }}
    >
      <div
        data-city-label
        data-location={region.location}
        className={`absolute flex flex-col whitespace-nowrap ${SIDE_CLASS[side]}`}
        style={sideOffset(side, offset)}
      >
        {region.isSelf && (
          <span
            data-here-chip
            className={`mb-1 rounded-full bg-[#E6F59E] text-center font-semibold tracking-[0.12em] text-[#0c1d31] uppercase shadow-[0_0_28px_rgba(230,245,158,0.5)] ${
              compact ? 'text-[9px]' : 'text-[10px]'
            }`}
            style={{ width: chip.width, height: chip.height, lineHeight: `${chip.height}px` }}
          >
            You are here
          </span>
        )}
        <span
          className={`leading-tight font-medium tracking-[-0.01em] transition-colors duration-500 ${
            compact ? 'text-[12px]' : 'text-[15px]'
          } ${focused ? 'text-white' : 'text-white/80'} [text-shadow:0_1px_10px_rgba(12,29,49,0.95)]`}
        >
          {region.city}
        </span>
        <span
          className={`font-medium tracking-[0.14em] text-[#E6F59E]/65 uppercase [text-shadow:0_1px_8px_rgba(12,29,49,0.95)] ${
            compact ? 'text-[9px]' : 'text-[10.5px]'
          }`}
        >
          {region.location}
        </span>
        {detail && (
          <span
            className={`text-white/50 [text-shadow:0_1px_8px_rgba(12,29,49,0.95)] ${
              compact ? 'text-[9.5px]' : 'text-[11px]'
            }`}
          >
            {detail}
          </span>
        )}
      </div>
    </motion.div>
  );
}

function ArcLabel({ link, focus, at }: { link: Link; focus: string; at: [number, number] }) {
  const [from, to] = link.a.location === focus ? [link.a, link.b] : [link.b, link.a];
  const color = STATE_COLOR[link.state];
  return (
    <motion.div
      className="absolute top-0 left-0"
      style={{ x: at[0], y: at[1] }}
      initial={{ opacity: 0, scale: 0.9 }}
      animate={{ opacity: 1, scale: 1 }}
      exit={{ opacity: 0, scale: 0.95 }}
      transition={{ duration: 0.45, ease: EASE_OUT }}
    >
      <div className="flex -translate-x-1/2 -translate-y-1/2 items-center gap-2 rounded-full border border-white/10 bg-[#0c1d31]/90 py-1 pr-3 pl-2 text-[12px] whitespace-nowrap shadow-[0_8px_30px_rgba(0,0,0,0.35)]">
        <span className="size-1.5 rounded-full" style={{ background: color, boxShadow: `0 0 8px ${color}` }} />
        <span className="text-white/80">
          {from.city} <span className="text-white/40">↔</span> {to.city}
        </span>
        <span className="text-white/30">·</span>
        <span className="font-medium tabular-nums" style={{ color: link.state === 'up' ? '#E6F59E' : color }}>
          {arcLabelText(link)}
        </span>
      </div>
    </motion.div>
  );
}

/** Zoom buttons, and the way back to the whole fleet once it is off screen. */
function ZoomControls({
  camera,
  compact,
  steerable,
}: {
  camera: ReturnType<typeof useMapCamera>;
  compact: boolean;
  /** False while something else is driving, which hides the way back. */
  steerable: boolean;
}) {
  const size = compact ? 'size-9' : 'size-11';
  const stop = (fn: () => void) => (e: React.MouseEvent) => {
    e.stopPropagation();
    fn();
  };
  return (
    <div className={`absolute z-30 flex flex-col items-end gap-2 ${compact ? 'right-2.5 bottom-2.5' : 'right-4 bottom-4'}`}>
      <AnimatePresence>
        {camera.engaged && steerable && (
          <motion.button
            type="button"
            initial={{ opacity: 0, y: 6 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: 6 }}
            onClick={stop(() => camera.reset())}
            className="rounded-full border border-white/10 bg-[#0c1d31]/85 px-3.5 py-2 text-[12px] font-medium text-white/80 transition-colors hover:bg-[#0c1d31] hover:text-white"
          >
            Whole fleet
          </motion.button>
        )}
      </AnimatePresence>
      <div className="flex flex-col overflow-hidden rounded-xl border border-white/10 bg-[#0c1d31]/85">
        <button
          type="button"
          aria-label="Zoom in"
          disabled={!camera.canZoomIn}
          onClick={stop(() => camera.zoomBy(ZOOM_STEP, undefined, true))}
          className={`${size} grid place-items-center text-white/70 transition-colors hover:bg-white/10 hover:text-white disabled:text-white/20`}
        >
          <svg viewBox="0 0 16 16" className="size-4" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
            <path d="M8 3v10M3 8h10" />
          </svg>
        </button>
        <button
          type="button"
          aria-label="Zoom out"
          disabled={!camera.engaged}
          onClick={stop(() => camera.zoomBy(1 / ZOOM_STEP, undefined, true))}
          className={`${size} grid place-items-center border-t border-white/10 text-white/70 transition-colors hover:bg-white/10 hover:text-white disabled:text-white/20`}
        >
          <svg viewBox="0 0 16 16" className="size-4" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
            <path d="M3 8h10" />
          </svg>
        </button>
      </div>
    </div>
  );
}

/** Steps through a set of labels so only one shows at a time. */
function useSpotlight(count: number, intervalMs = 3200): number {
  const [index, setIndex] = useState(0);
  useEffect(() => {
    if (count <= 1) return;
    const id = window.setInterval(() => setIndex((i) => i + 1), intervalMs);
    return () => window.clearInterval(id);
  }, [count, intervalMs]);
  return index;
}
