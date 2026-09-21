import { type RefObject, useEffect, useState } from 'react';

/**
 * How many rows a feed shows: as many as fit the space it was given.
 *
 * The two feeds are the most alive things on the page, so where a layout hands
 * them a tall column they should fill it rather than showing the same four
 * lines they show on a phone. Where the column has no height of its own — one
 * list after another down a scrolling page — there is nothing to measure and
 * the caller's figure stands.
 */
export function useFittedRows(
  list: RefObject<HTMLElement | null>,
  fill: boolean,
  rowHeight: number,
  limit: number,
  max: number
): number {
  const [fitted, setFitted] = useState(limit);
  useEffect(() => {
    if (!fill) return;
    const el = list.current;
    if (!el) return;
    // The list is a flex child of a column with a height, so what it measures
    // is the room it has rather than the rows already in it.
    // One row past the edge, so the list fades into more rather than stopping
    // short of the bottom of its own card.
    const measure = () => {
      const rows = Math.ceil(el.getBoundingClientRect().height / rowHeight) + 1;
      setFitted(Math.max(4, Math.min(max, rows)));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [list, fill, rowHeight, max]);
  return fill ? fitted : limit;
}

/**
 * How tall a block should be to reach the bottom of the window. Used for the
 * pair of feeds under a stacked map, so what the platform is doing lands on
 * the first screen rather than below it. Measured from the document rather
 * than the viewport, so scrolling does not resize it.
 */
export function useFillHeight(
  block: RefObject<HTMLElement | null>,
  enabled: boolean,
  min: number
): number | undefined {
  const [height, setHeight] = useState<number | undefined>(undefined);
  useEffect(() => {
    if (!enabled) {
      setHeight(undefined);
      return;
    }
    const el = block.current;
    if (!el) return;
    const measure = () => {
      const top = el.getBoundingClientRect().top + window.scrollY;
      const next = Math.round(Math.max(min, window.innerHeight - top - 24));
      // A few pixels either way is the block's own height settling, not the
      // page changing shape; taking it would start the measurement over.
      setHeight((current) => (current !== undefined && Math.abs(current - next) < 10 ? current : next));
    };
    measure();
    const ro = new ResizeObserver(measure);
    if (el.parentElement) ro.observe(el.parentElement);
    window.addEventListener('resize', measure);
    return () => {
      ro.disconnect();
      window.removeEventListener('resize', measure);
    };
  }, [block, enabled, min]);
  return enabled ? height : undefined;
}
