import { useEffect, useState } from 'react';

/**
 * Layouts the page switches between. Each is designed on its own terms rather
 * than being a shrunken version of the one above it.
 *
 * - desktop:   map beside the panel, the projector view.
 * - landscape: a short, wide phone; same two columns, tighter.
 * - tablet:    map above a stacked panel, roomy.
 * - phone:     map above a stacked panel, compact.
 */
export type LayoutMode = 'desktop' | 'landscape' | 'tablet' | 'phone';

export interface Viewport {
  width: number;
  height: number;
  mode: LayoutMode;
  /** Small screens drop per-edge labels, shrink type and animate less. */
  compact: boolean;
  stacked: boolean;
  /**
   * How many columns the content under a stacked map reads in. Only the
   * stacked layouts use it; beside the map there is one column by definition.
   */
  columns: 1 | 2;
}

/**
 * What the side-by-side layout costs the map, in pixels: the panel beside it
 * and the page margins come off the width, the header and the line under the
 * map off the height. Approximate, and only used to judge a shape.
 */
const PANEL_AND_MARGINS = 556;
const HEADER_AND_STRIP = 224;

/**
 * The shallowest column worth putting a map in. The fleet is a wide, short
 * shape, so anything squarer than this letterboxes it however it is framed.
 */
export const MIN_MAP_ASPECT = 1.2;

/**
 * How wide a column of the page's content reads at, and the gutters and gap
 * around a pair of them. Roughly the width the panel gets beside the map,
 * which is the width the tiles and the lists were drawn for.
 */
const CONTENT_COLUMN = 460;
const CONTENT_GUTTERS = 56;

function measure(): Viewport {
  const width = window.innerWidth;
  const height = window.innerHeight;
  // A window taller than it is wide has no room for a map beside the panel,
  // however many pixels it has: what is left over is a tall slot the fleet
  // cannot fill. So the layout follows the shape of that slot rather than the
  // window's pixel count.
  const column = (width - PANEL_AND_MARGINS) / Math.max(1, height - HEADER_AND_STRIP);
  let mode: LayoutMode;
  if (width >= 1100 && height >= 620 && column >= MIN_MAP_ASPECT) mode = 'desktop';
  else if (height < 620 && width >= 700) mode = 'landscape';
  else if (width >= 700) mode = 'tablet';
  else mode = 'phone';
  // Almost nothing under the map is prose: it is number tiles and two lists,
  // and they go on reading as far as a column of them fits. So the stacked
  // layout asks how many columns the window holds rather than being held to
  // one measure from a phone all the way up to a portrait desktop.
  const columns = width >= 2 * CONTENT_COLUMN + CONTENT_GUTTERS ? 2 : 1;
  return {
    width,
    height,
    mode,
    compact: mode !== 'desktop',
    stacked: mode === 'tablet' || mode === 'phone',
    columns,
  };
}

export function useViewport(): Viewport {
  const [vp, setVp] = useState(measure);
  useEffect(() => {
    const onChange = () => setVp(measure());
    window.addEventListener('resize', onChange);
    window.addEventListener('orientationchange', onChange);
    return () => {
      window.removeEventListener('resize', onChange);
      window.removeEventListener('orientationchange', onChange);
    };
  }, []);
  return vp;
}

/** True when the visitor asked for less motion. */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(
    () => typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
  );
  useEffect(() => {
    const query = window.matchMedia('(prefers-reduced-motion: reduce)');
    const onChange = () => setReduced(query.matches);
    query.addEventListener('change', onChange);
    return () => query.removeEventListener('change', onChange);
  }, []);
  return reduced;
}
