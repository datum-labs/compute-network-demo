import { AnimatePresence, motion } from 'motion/react';
import { useRef } from 'react';
import type { FeedEntry } from '../useFeed';
import { useFittedRows } from '../useRows';
import { PanelHeading } from './PanelHeading';

interface Props {
  entries: FeedEntry[];
  /** The instance serving this page, for the pinned "your request" line. */
  selfCity: string | null;
  selfLocation?: string | null;
  selfAddress?: string | null;
  /** When the page last heard from the fleet. */
  servedAt: number;
  compact?: boolean;
  reducedMotion?: boolean;
  /** Rows to show where the list has no height of its own to fill. */
  limit?: number;
  /** True when the list was given a height, so it shows as many rows as fit. */
  fill?: boolean;
}

/** What one line of traffic measures, for working out how many will fit. */
const ROW_HEIGHT = 30;
const COMPACT_ROW_HEIGHT = 26;
/** Beyond this the oldest line is older than the feed's own memory. */
const MAX_ROWS = 24;

const clock = new Intl.DateTimeFormat('en-GB', {
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
});

function formatTime(iso: string): string {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? '--:--:--' : clock.format(new Date(t));
}

/**
 * A feed of the messages instances just exchanged with each other. It is the
 * proof that the arcs on the map are real traffic and not an animation.
 */
export function FeedPanel({
  entries,
  selfCity,
  selfLocation,
  selfAddress,
  servedAt,
  compact = false,
  reducedMotion = false,
  limit = 9,
  fill = false,
}: Props) {
  const list = useRef<HTMLUListElement>(null);
  const fitted = useFittedRows(list, fill, compact ? COMPACT_ROW_HEIGHT : ROW_HEIGHT, limit, MAX_ROWS);
  const rows = entries.slice(0, fitted);
  const timeClass = compact ? 'text-[10.5px]' : 'text-[11.5px]';
  const textClass = compact ? 'text-[12.5px]' : 'text-[13.5px]';

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-baseline justify-between">
        <PanelHeading>Live traffic</PanelHeading>
        <span className="flex items-center gap-1.5 text-[11px] text-white/35">
          <span className="size-1.5 rounded-full bg-[#B3D56F] shadow-[0_0_8px_#B3D56F]" />
          now
        </span>
      </div>

      {/* The visitor's own request, kept in view above the fleet's traffic. */}
      <div
        className={`mt-2.5 flex items-start gap-3 rounded-xl border border-[#E6F59E]/25 bg-[#E6F59E]/[0.07] px-3 ${
          compact ? 'py-2' : 'py-2.5'
        }`}
      >
        <span className={`shrink-0 pt-px font-mono text-[#E6F59E]/60 tabular-nums ${timeClass}`}>
          {formatTime(new Date(servedAt).toISOString())}
        </span>
        <span className="min-w-0 flex-1">
          <span className={`block truncate font-medium text-[#E6F59E] ${textClass}`}>
            Your request was served from {selfCity ?? 'the nearest region'}
          </span>
          {selfLocation && (
            <span className={`block truncate font-mono text-white/40 ${timeClass}`}>
              {selfLocation}
              {selfAddress && ` · ${selfAddress}`}
            </span>
          )}
        </span>
      </div>

      <ul
        ref={list}
        className={`mt-1 min-h-0 flex-1 overflow-hidden ${compact ? '' : '[mask-image:linear-gradient(to_bottom,#000_86%,transparent)]'}`}
      >
        <AnimatePresence initial={false}>
          {rows.map((e) => (
            <motion.li
              key={e.key}
              initial={reducedMotion ? false : { opacity: 0, y: -6 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, transition: { duration: 0.15 } }}
              transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
              className={`flex items-center gap-3 border-b border-white/[0.04] ${compact ? 'py-[5px]' : 'py-[7px]'}`}
            >
              <span className={`shrink-0 font-mono text-white/35 tabular-nums ${timeClass}`}>{formatTime(e.at)}</span>
              <span className={`min-w-0 flex-1 truncate text-white/80 ${textClass}`}>
                {e.fromCity} <span className="text-white/35">→</span> {e.toCity}
              </span>
              {e.ok ? (
                <span className={`shrink-0 font-medium text-white/90 tabular-nums ${textClass}`}>
                  {e.rttMs < 10 ? e.rttMs.toFixed(1) : Math.round(e.rttMs)} ms
                </span>
              ) : (
                <span className={`shrink-0 font-medium text-[#EF7B6C] ${textClass}`}>failed</span>
              )}
            </motion.li>
          ))}
        </AnimatePresence>
        {rows.length === 0 && (
          <li className={`py-2 text-white/40 ${textClass}`}>Waiting for the first messages between Instances…</li>
        )}
      </ul>
    </div>
  );
}
