import { AnimatePresence, motion } from 'motion/react';
import { memo, useRef } from 'react';
import type { Activity, MeshView } from '../types';
import { useNow } from '../useMesh';
import { useFittedRows } from '../useRows';
import { PanelHeading } from './PanelHeading';

interface Props {
  activity: Activity[];
  /** Every Instance's join time, so the headline has a number before anything scales. */
  view: MeshView | null;
  /** Browser time when the current view arrived, for honest "18s ago" labels. */
  servedAt: number;
  compact?: boolean;
  reducedMotion?: boolean;
  /** Rows to show where the list has no height of its own to fill. */
  limit?: number;
  /** True when the list was given a height, so it shows as many rows as fit. */
  fill?: boolean;
}

/** What one event measures, for working out how many will fit. */
const ROW_HEIGHT = 47;
const COMPACT_ROW_HEIGHT = 43;
/** No more than the fleet view carries, or the list would end in blanks. */
const MAX_ROWS = 14;

/** How each kind of event reads, and the colour it carries. */
const TONE = {
  'scaled-up': '#E6F59E',
  'scaled-down': '#8FA3B8',
  'instance-starting': '#F2C46D',
  'instance-ready': '#B3D56F',
  'instance-stopping': '#8FA3B8',
} as const;

const formatSeconds = (ms: number) => `${(ms / 1000).toFixed(1)}s`;

/**
 * Plain-language sentences, written here rather than served: the API carries
 * the facts so the wording can be tuned without a redeploy of the fleet.
 */
function phrase(e: Activity): { title: string; detail?: string; highlight?: boolean } {
  const where = e.city || e.location;
  switch (e.type) {
    case 'scaled-up':
      return { title: `Demand is up in ${where}`, detail: `scaling to ${e.to} Instances` };
    case 'scaled-down':
      return { title: `Demand has eased in ${where}`, detail: `back to ${e.to} Instances` };
    case 'instance-starting':
      return { title: `An Instance is starting in ${where}`, detail: e.location };
    case 'instance-ready':
      return {
        title: `A new Instance in ${where}`,
        detail: e.joinMs ? `joined the private network in ${formatSeconds(e.joinMs)}` : 'joined the private network',
        highlight: true,
      };
    case 'instance-stopping':
      return { title: `An Instance in ${where} is winding down`, detail: 'its traffic is moving to the others' };
  }
}

/** Activity is minutes apart, so it reads better as "18s ago" than as a clock. */
function ago(iso: string, now: number): string {
  const seconds = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (!Number.isFinite(seconds)) return '';
  if (seconds < 5) return 'just now';
  if (seconds < 60) return `${seconds}s ago`;
  return `${Math.floor(seconds / 60)}m ago`;
}

/**
 * How long a new Instance takes to become reachable by every peer over the
 * private network. It is the number this whole demo exists to show, so it is
 * averaged across the fleet rather than taken from whichever Instance happened
 * to start last.
 */
function joinAverage(activity: Activity[], view: MeshView | null): number {
  const joined = activity.filter((e) => e.type === 'instance-ready' && e.joinMs).map((e) => e.joinMs!);
  const known = joined.length ? joined : (view?.instances ?? []).map((i) => i.joinMs ?? 0).filter((ms) => ms > 0);
  if (known.length === 0) return 0;
  return known.reduce((sum, ms) => sum + ms, 0) / known.length;
}

/**
 * A feed of what the platform did by itself: workloads scaling with demand and
 * the Instances that come and go with them. Kept apart from the traffic feed —
 * that one shows the mesh working, this one shows it changing shape.
 */
export const ActivityPanel = memo(ActivityPanelImpl);

function ActivityPanelImpl({
  activity,
  view,
  servedAt,
  compact = false,
  reducedMotion = false,
  limit = 5,
  fill = false,
}: Props) {
  // Relative times have to keep moving between polls.
  useNow(1000);
  const list = useRef<HTMLUListElement>(null);
  const fitted = useFittedRows(list, fill, compact ? COMPACT_ROW_HEIGHT : ROW_HEIGHT, limit, MAX_ROWS);
  const rows = activity.slice(0, fitted);
  const join = joinAverage(activity, view);
  // Events are stamped by the fleet, so they are read against the fleet's
  // clock: a visitor whose machine is a minute out still sees "just now".
  const drift = view ? servedAt - Date.parse(view.generatedAt) : 0;
  const clock = Date.now() - (Number.isFinite(drift) ? drift : 0);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-baseline justify-between">
        <PanelHeading>Platform activity</PanelHeading>
        <span className="text-[11px] text-white/35">scales itself</span>
      </div>

      {/* The line an investor should leave with, and the reason the lifecycle
          is simulated at all rather than instances simply appearing. */}
      <div
        className="mt-2.5 flex items-center gap-3.5 rounded-xl border border-[#E6F59E]/25 bg-[#E6F59E]/[0.07] px-3.5 py-2.5"
      >
        <span
          className={`leading-none font-medium tracking-[-0.03em] text-[#E6F59E] tabular-nums ${
            compact ? 'text-[26px]' : 'text-[32px]'
          }`}
        >
          {join > 0 ? formatSeconds(join) : '—'}
        </span>
        {/* The one sentence in the panel, held to a measure: everything else
            here is a number or a name and reads at any width. */}
        <span className={`min-w-0 flex-1 leading-snug text-white/70 max-w-[54ch] ${compact ? 'text-[11.5px]' : 'text-[12.5px]'}`}>
          is how long a new Instance takes to reach every peer.
          <span className="text-white/45"> Datum sets up that network.</span>
        </span>
      </div>

      <ul
        ref={list}
        className={`mt-1 min-h-0 flex-1 overflow-hidden ${compact ? '' : '[mask-image:linear-gradient(to_bottom,#000_88%,transparent)]'}`}
      >
        {/* Scaling arrives in bursts, so a whole panel can turn over at once.
            Exiting rows leave the flow immediately and arriving ones are
            already legible, rather than the list blinking empty. */}
        <AnimatePresence initial={false} mode="popLayout">
          {rows.map((e) => {
            const { title, detail, highlight } = phrase(e);
            return (
              <motion.li
                key={`${e.at}|${e.type}|${e.instance ?? e.location}`}
                layout={!reducedMotion}
                initial={reducedMotion ? false : { opacity: 0.25, y: -8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, transition: { duration: 0.12 } }}
                transition={{ duration: 0.3, ease: [0.22, 1, 0.36, 1] }}
                className="flex items-start gap-2.5 border-b border-white/[0.04] py-2"
              >
                <span
                  className="mt-[6px] size-1.5 shrink-0 rounded-full"
                  style={{ background: TONE[e.type], boxShadow: `0 0 8px ${TONE[e.type]}` }}
                />
                <span className="min-w-0 flex-1">
                  <span className={`block truncate font-medium text-white/90 ${compact ? 'text-[12.5px]' : 'text-[13.5px]'}`}>
                    {title}
                  </span>
                  {detail && (
                    <span
                      className={`block truncate ${compact ? 'text-[11px]' : 'text-[12px]'} ${
                        highlight ? 'text-[#E6F59E]' : 'text-white/45'
                      }`}
                    >
                      {detail}
                    </span>
                  )}
                </span>
                <span className={`shrink-0 pt-[1px] text-white/30 tabular-nums ${compact ? 'text-[10.5px]' : 'text-[11.5px]'}`}>
                  {ago(e.at, clock)}
                </span>
              </motion.li>
            );
          })}
        </AnimatePresence>
        {rows.length === 0 && (
          <li className={`py-2 text-white/40 ${compact ? 'text-[12.5px]' : 'text-[13.5px]'}`}>
            Waiting for the fleet to change shape…
          </li>
        )}
      </ul>
    </div>
  );
}
