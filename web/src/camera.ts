import { type RefObject, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { MAP_HEIGHT, MAP_WIDTH } from './mesh';
import type { Region } from './types';

/** The part of the map in view, in map units. */
export interface Camera {
  x: number;
  y: number;
  w: number;
  h: number;
}

// How close the auto-fit framing will go, in screen pixels per map unit. Three
// locations in one country need much of this: at the old global-fleet ceiling
// the whole fleet sat in a corner of an empty ocean.
const FIT_PIXELS_PER_UNIT = 3.6;
/**
 * How far in the visitor may zoom, as a multiple of the whole-fleet framing.
 * A multiple rather than a scale, because what counts is how much closer this
 * is than the view the page opens on, whatever screen it opened on. It has to
 * clear the walkthrough's close-up, which frames one location's Instances.
 */
export const MAX_ZOOM = 10;
/** However far in, never frame less of the world than this, in map units. */
const MIN_CAMERA_WIDTH = 30;
export const FULL_MAP: Camera = { x: 0, y: 0, w: MAP_WIDTH, h: MAP_HEIGHT };

/**
 * Frames the fleet with room for labels, in the aspect ratio of the space the
 * map is given. A fleet in one part of the world is shown up close; a global
 * one shows as much of the map as the shape allows. On a narrow screen this
 * crops the empty polar bands rather than shrinking everything.
 */
export function fitCamera(regions: Region[], aspect: number, pixelWidth: number, compact = false): Camera {
  const minWidth = pixelWidth > 0 ? pixelWidth / FIT_PIXELS_PER_UNIT : MAP_WIDTH / 1.75;
  if (regions.length === 0) return frame(MAP_WIDTH / 2, MAP_HEIGHT / 2, MAP_WIDTH, aspect);

  const b = fleetBounds(regions, compact);
  const w = Math.max(b.width, b.height * aspect, minWidth);
  // Sit the fleet slightly below centre; labels and chips extend upwards.
  return frame(b.cx, b.cy - w / aspect / 25, w, aspect);
}

/** The fleet's extent plus room for its labels, in map units. */
function fleetBounds(regions: Region[], compact: boolean) {
  const xs = regions.map((r) => r.x);
  const ys = regions.map((r) => r.y);
  const minX = Math.min(...xs);
  const maxX = Math.max(...xs);
  const minY = Math.min(...ys);
  const maxY = Math.max(...ys);
  const padX = compact ? Math.max(46, (maxX - minX) * 0.1) : Math.max(110, (maxX - minX) * 0.22);
  const padY = compact ? Math.max(46, (maxY - minY) * 0.18) : Math.max(80, (maxY - minY) * 0.3);
  // Arcs bow above the pins that carry them, so the drawing is taller than
  // the fleet's own extent.
  const bow = Math.min(110, (maxX - minX) * 0.24);
  return {
    width: maxX - minX + 2 * padX,
    height: maxY - minY + bow + 2 * padY,
    cx: (minX + maxX) / 2,
    cy: (minY + maxY) / 2,
  };
}

/**
 * The shape the map should be given when it sits above the panel: wide and
 * short for a fleet spread around the equator, squarer for a regional one.
 * Without this a fixed-height box leaves the map stranded in empty ocean.
 */
export function fleetAspect(regions: Region[], compact = true): number {
  if (regions.length < 2) return MAP_WIDTH / MAP_HEIGHT;
  const b = fleetBounds(regions, compact);
  return Math.min(3.2, Math.max(1.1, b.width / b.height));
}

/** Builds a camera of the requested width around a centre, kept on the map. */
function frame(cx: number, cy: number, width: number, aspect: number): Camera {
  let w = Math.min(width, MAP_WIDTH);
  let h = w / aspect;
  if (h > MAP_HEIGHT) {
    h = MAP_HEIGHT;
    w = h * aspect;
  }
  return {
    x: clamp(cx - w / 2, 0, MAP_WIDTH - w),
    y: clamp(cy - h / 2, 0, MAP_HEIGHT - h),
    w,
    h,
  };
}

function clamp(v: number, lo: number, hi: number) {
  return Math.min(Math.max(v, lo), Math.max(lo, hi));
}

const easeInOut = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);

/** Glides the camera to a new framing instead of cutting. */
export function useCameraTween(target: Camera, durationMs = 1600): Camera {
  const [cam, setCam] = useState(target);
  const current = useRef(target);

  useEffect(() => {
    const from = current.current;
    // Relative, so that a drag at close range still moves the map: half a map
    // unit is a big step across the world and a sub-pixel one over a city.
    const epsilon = Math.min(0.5, target.w / 2000);
    const same =
      Math.abs(from.x - target.x) < epsilon &&
      Math.abs(from.y - target.y) < epsilon &&
      Math.abs(from.w - target.w) < epsilon;
    if (same) return;
    if (durationMs <= 0) {
      current.current = target;
      setCam(target);
      return;
    }
    const start = performance.now();
    let raf = 0;
    const step = (now: number) => {
      const t = Math.min(1, (now - start) / durationMs);
      const e = easeInOut(t);
      const next = {
        x: from.x + (target.x - from.x) * e,
        y: from.y + (target.y - from.y) * e,
        w: from.w + (target.w - from.w) * e,
        h: from.h + (target.h - from.h) * e,
      };
      current.current = next;
      setCam(next);
      if (t < 1) raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [target.x, target.y, target.w, target.h, durationMs]); // eslint-disable-line react-hooks/exhaustive-deps

  return cam;
}

/** How much one press of the on-screen zoom buttons changes the scale. */
export const ZOOM_STEP = 1.9;

/** How long the camera takes to fly somewhere it was sent, rather than dragged. */
export const FLIGHT_MS = 700;

/** Driving the camera by hand. */
export interface MapCamera {
  /** The element gestures are read from. */
  surface: RefObject<HTMLDivElement | null>;
  /** Where the camera is now, mid-flight. */
  cam: Camera;
  /** Where it is heading, which is what labels are laid out against. */
  target: Camera;
  /** True while the visitor is steering instead of the auto-fit framing. */
  engaged: boolean;
  /** How much closer the target framing is than the whole-fleet one. */
  zoom: number;
  /** True when there is still room to zoom in or out. */
  canZoomIn: boolean;
  zoomBy: (factor: number, anchorPx?: [number, number], smooth?: boolean) => void;
  panBy: (dxPx: number, dyPx: number) => void;
  /**
   * Flies in on one location, close enough for its replicas to fan out. A
   * visitor who asked for less motion gets the framing without the flight.
   */
  focusRegion: (region: Region, closeness?: number, animate?: boolean) => void;
  /** Returns to the whole fleet and hands the camera back to the auto-fit. */
  reset: (animate?: boolean) => void;
  /** True when the gesture that just ended moved the map, so it was not a tap. */
  dragged: () => boolean;
}

/** Past this many pixels a press counts as a drag rather than a tap. */
const DRAG_SLOP = 6;

/**
 * Lets the visitor zoom and pan, while the auto-fit framing stays in charge
 * whenever they are not. Interaction hands control over immediately; the
 * camera returns to the whole fleet after the page has been left alone, so an
 * unattended screen goes back to the view that explains itself. An idleMs of
 * zero holds that clock off, for while something else is driving the camera.
 */
export function useMapCamera(fit: Camera, width: number, height: number, idleMs: number): MapCamera {
  const [override, setOverride] = useState<Camera | null>(null);
  const [smooth, setSmooth] = useState(true);
  const idle = useRef(0);
  const surface = useRef<HTMLDivElement | null>(null);
  const travelled = useRef(0);
  // A gesture fires several times before React re-renders, so each step builds
  // on the framing the last one produced rather than on the rendered state.
  const overrideRef = useRef<Camera | null>(null);
  const fitRef = useRef(fit);
  fitRef.current = fit;

  const target = override ?? fit;
  const cam = useCameraTween(target, smooth ? (override ? FLIGHT_MS : 1600) : 0);

  const reset = useCallback((animate: boolean = true) => {
    window.clearTimeout(idle.current);
    overrideRef.current = null;
    setSmooth(animate);
    setOverride(null);
  }, []);

  // Any steering restarts the clock that eventually gives the view back.
  const hold = useCallback(() => {
    window.clearTimeout(idle.current);
    if (idleMs > 0) idle.current = window.setTimeout(reset, idleMs);
  }, [idleMs, reset]);

  useEffect(() => () => window.clearTimeout(idle.current), []);

  // Whoever was driving has let go, so the clock starts from wherever they
  // left the camera rather than from the next gesture.
  useEffect(() => {
    if (overrideRef.current) hold();
  }, [hold]);

  const apply = useCallback(
    (next: Camera, flying: boolean) => {
      const current = fitRef.current;
      // Zooming back out past the fleet is how the visitor says "show me
      // everything", so it lands exactly on the auto-fit framing.
      if (next.w >= current.w - 0.5) {
        reset();
        return;
      }
      overrideRef.current = next;
      setSmooth(flying);
      setOverride(next);
      hold();
    },
    [hold, reset]
  );

  const zoomBy = useCallback(
    (factor: number, anchorPx?: [number, number], flying = false) => {
      if (width <= 0 || height <= 0) return;
      const from = overrideRef.current ?? fitRef.current;
      const w = clamp(from.w / factor, closest(fitRef.current), fitRef.current.w);
      const h = (w * from.h) / from.w;
      const [ax, ay] = anchorPx ?? [width / 2, height / 2];
      // Hold whatever is under the pointer still while the scale changes.
      const ux = from.x + (ax / width) * from.w;
      const uy = from.y + (ay / height) * from.h;
      apply(contain({ x: ux - (ax / width) * w, y: uy - (ay / height) * h, w, h }), flying);
    },
    [apply, width, height]
  );

  const panBy = useCallback(
    (dxPx: number, dyPx: number) => {
      if (width <= 0) return;
      const from = overrideRef.current;
      if (!from) return;
      const scale = from.w / width;
      apply(contain({ ...from, x: from.x - dxPx * scale, y: from.y - dyPx * scale }), false);
    },
    [apply, width]
  );

  const focusRegion = useCallback(
    (region: Region, closeness = 0.36, animate = true) => {
      if (width <= 0) return;
      const w = clamp(fitRef.current.w * closeness, closest(fitRef.current), fitRef.current.w);
      const h = (w * fitRef.current.h) / fitRef.current.w;
      apply(contain({ x: region.x - w / 2, y: region.y - h / 2, w, h }), animate);
    },
    [apply, width]
  );

  useGestures(surface, travelled, { zoomBy, panBy, engaged: override !== null });

  const dragged = useCallback(() => travelled.current > DRAG_SLOP, []);
  const zoom = fit.w / target.w;
  return useMemo(
    () => ({
      surface,
      cam,
      target,
      engaged: override !== null,
      zoom,
      canZoomIn: target.w > closest(fit) + 0.5,
      zoomBy,
      panBy,
      focusRegion,
      reset,
      dragged,
    }),
    [cam, target, override, zoom, fit, zoomBy, panBy, focusRegion, reset, dragged]
  );
}

interface GestureApi {
  zoomBy: (factor: number, anchorPx?: [number, number]) => void;
  panBy: (dxPx: number, dyPx: number) => void;
  engaged: boolean;
}

/**
 * Wheel, trackpad pinch, touch pinch and drag-to-pan on the map surface.
 *
 * On a stacked layout the map sits above a page that scrolls, so one finger is
 * left to the page until the visitor has zoomed in; from then on the map takes
 * the drag and pans. Two fingers always mean the map, which is why the
 * surface never carries plain touch-action: auto.
 */
function useGestures(
  surface: RefObject<HTMLDivElement | null>,
  travelled: RefObject<number>,
  api: GestureApi
) {
  const latest = useRef(api);
  latest.current = api;

  useEffect(() => {
    const el = surface.current;
    if (!el) return;
    const at = (clientX: number, clientY: number): [number, number] => {
      const r = el.getBoundingClientRect();
      return [clientX - r.left, clientY - r.top];
    };
    const spread = (touches: TouchList) => {
      const [a, b] = [touches[0], touches[1]];
      return {
        distance: Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY) || 1,
        centre: [(a.clientX + b.clientX) / 2, (a.clientY + b.clientY) / 2] as [number, number],
      };
    };

    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      // A trackpad pinch arrives as a wheel event with ctrlKey set, and its
      // deltas are far smaller than a mouse wheel's notches.
      latest.current.zoomBy(Math.exp(-e.deltaY * (e.ctrlKey ? 0.012 : 0.0022)), at(e.clientX, e.clientY));
    };

    let pinch: ReturnType<typeof spread> | null = null;
    let finger: [number, number] | null = null;

    const onTouchStart = (e: TouchEvent) => {
      travelled.current = 0;
      pinch = e.touches.length === 2 ? spread(e.touches) : null;
      finger = e.touches.length === 1 && latest.current.engaged ? [e.touches[0].clientX, e.touches[0].clientY] : null;
    };
    const onTouchMove = (e: TouchEvent) => {
      if (e.touches.length === 2) {
        const next = spread(e.touches);
        if (pinch) {
          e.preventDefault();
          travelled.current += Math.abs(next.distance - pinch.distance) + 1;
          latest.current.zoomBy(next.distance / pinch.distance, at(...next.centre));
          latest.current.panBy(next.centre[0] - pinch.centre[0], next.centre[1] - pinch.centre[1]);
        }
        pinch = next;
        return;
      }
      if (finger && e.touches.length === 1) {
        e.preventDefault();
        const dx = e.touches[0].clientX - finger[0];
        const dy = e.touches[0].clientY - finger[1];
        travelled.current += Math.abs(dx) + Math.abs(dy);
        latest.current.panBy(dx, dy);
        finger = [e.touches[0].clientX, e.touches[0].clientY];
      }
    };
    const onTouchEnd = (e: TouchEvent) => {
      if (e.touches.length < 2) pinch = null;
      if (e.touches.length === 0) finger = null;
    };

    const onPointerDown = (e: PointerEvent) => {
      if (e.pointerType !== 'mouse' || e.button !== 0) return;
      travelled.current = 0;
      if (!latest.current.engaged) return;
      let last: [number, number] = [e.clientX, e.clientY];
      const move = (m: PointerEvent) => {
        const dx = m.clientX - last[0];
        const dy = m.clientY - last[1];
        travelled.current += Math.abs(dx) + Math.abs(dy);
        latest.current.panBy(dx, dy);
        last = [m.clientX, m.clientY];
      };
      const up = () => {
        window.removeEventListener('pointermove', move);
        window.removeEventListener('pointerup', up);
      };
      window.addEventListener('pointermove', move);
      window.addEventListener('pointerup', up);
    };

    el.addEventListener('wheel', onWheel, { passive: false });
    el.addEventListener('touchstart', onTouchStart, { passive: false });
    el.addEventListener('touchmove', onTouchMove, { passive: false });
    el.addEventListener('touchend', onTouchEnd);
    el.addEventListener('pointerdown', onPointerDown);
    return () => {
      el.removeEventListener('wheel', onWheel);
      el.removeEventListener('touchstart', onTouchStart);
      el.removeEventListener('touchmove', onTouchMove);
      el.removeEventListener('touchend', onTouchEnd);
      el.removeEventListener('pointerdown', onPointerDown);
    };
  }, [surface, travelled]);
}

/** The narrowest framing the visitor may zoom to. */
function closest(fit: Camera): number {
  return Math.max(fit.w / MAX_ZOOM, MIN_CAMERA_WIDTH);
}

/** Keeps a framing inside the map. */
function contain(c: Camera): Camera {
  return {
    ...c,
    x: clamp(c.x, 0, MAP_WIDTH - c.w),
    y: clamp(c.y, 0, MAP_HEIGHT - c.h),
  };
}
