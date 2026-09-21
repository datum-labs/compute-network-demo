import { Card } from '@datum-cloud/datum-ui/card';
import { AnimatePresence, motion } from 'motion/react';
import { useRef } from 'react';

import type { Link, MeshView, Region } from '../types';
import type { Feed } from '../useFeed';
import { ActivityPanel } from './ActivityPanel';
import { FeedPanel } from './FeedPanel';
import { LiveCounter } from './LiveCounter';
import { PanelHeading } from './PanelHeading';
import { useFillHeight } from '../useRows';

const EASE_OUT = [0.22, 1, 0.36, 1] as const;

/**
 * How short the pair of feeds may get when they run to the bottom of the
 * window. Below this they stop reading as feeds and start reading as a
 * caption, so the page scrolls a little instead.
 */
const MIN_FEEDS_HEIGHT = 420;

// No backdrop blur: what sits behind these panels is a smooth gradient, so
// blurring it cost a re-raster of every panel on every frame and looked the
// same either way.
const panelCard =
  'gap-0 rounded-2xl border-white/[0.08] bg-white/[0.035] py-0 text-white shadow-none';

interface Props {
  view: MeshView | null;
  regions: Region[];
  links: Link[];
  feed: Feed;
  /** When the page last heard from the fleet, for the visitor's own line. */
  servedAt: number;
  /** Rows of live traffic to show. */
  feedLimit?: number;
  /** Rows of platform activity to show; it moves far more slowly than traffic. */
  activityLimit?: number;
  reducedMotion?: boolean;
  /** Smaller type and padding on small screens. */
  compact?: boolean;
  /** Stacked below the map, so the list grows instead of scrolling inside. */
  stacked?: boolean;
  /**
   * Columns to lay the panels out in. Two puts the stat tiles in one row and
   * the two feeds side by side, which is what a wide stacked page has room
   * for — and it lifts the activity feed back above the fold.
   */
  columns?: 1 | 2;
}

export function StatsPanel({
  view,
  regions,
  links,
  feed,
  servedAt,
  feedLimit = 9,
  activityLimit = 5,
  reducedMotion = false,
  compact = false,
  stacked = false,
  columns = 1,
}: Props) {
  const wide = columns === 2;
  // Side by side under the map the pair of feeds runs to the bottom of the
  // window, so the platform reacting lands on the first screen. Either way
  // they then have a height of their own and fill it with rows.
  const feeds = useRef<HTMLDivElement>(null);
  const feedsHeight = useFillHeight(feeds, stacked && wide, MIN_FEEDS_HEIGHT);
  const fill = !stacked || feedsHeight !== undefined;
  const totals = view?.totals;
  const measured = links.filter((l) => l.state !== 'pending');
  const healthy = measured.filter((l) => l.state === 'up').length;
  const selfRegion = regions.find((r) => r.isSelf) ?? null;
  const selfInstance = selfRegion?.instances.find((i) => i.isSelf);

  const pad = compact ? 'px-4 py-3' : 'px-5 py-4';
  const headline = compact ? 'px-4 py-3' : 'px-5 py-3.5';

  const tiles = [
    { label: 'Regions live', value: totals?.regions ?? 0 },
    { label: 'Instances', value: totals?.instances ?? 0, tour: 'instances' as const },
    {
      label: 'Average latency',
      value:
        totals && totals.avgRttMs > 0
          ? totals.avgRttMs < 10
            ? totals.avgRttMs.toFixed(1)
            : Math.round(totals.avgRttMs)
          : '—',
      unit: totals && totals.avgRttMs > 0 ? 'ms' : undefined,
      // Latency moves on every poll; swapping it in would just flicker.
      animate: false,
    },
    {
      label: 'Connections',
      value: measured.length ? `${healthy}/${measured.length}` : '—',
      unit: measured.length ? 'healthy' : undefined,
      accent: measured.length > 0 && healthy < measured.length ? '#F2C46D' : undefined,
    },
  ];

  const gap = compact ? 'gap-2.5' : 'gap-4';

  const activityCard = (
    <Card className={`${panelCard} ${fill ? 'min-h-0 flex-1' : ''}`} data-tour="activity">
      <div className={`flex flex-col ${fill ? 'h-full min-h-0' : ''} ${pad}`}>
        <ActivityPanel
          activity={view?.activity ?? []}
          view={view}
          servedAt={servedAt}
          compact={compact}
          reducedMotion={reducedMotion}
          limit={activityLimit}
          fill={fill}
        />
      </div>
    </Card>
  );

  const trafficCard = (
    <Card className={`${panelCard} ${fill ? 'min-h-0 flex-1' : ''}`}>
      <div className={`flex flex-col ${fill ? 'h-full min-h-0' : ''} ${pad}`}>
        <FeedPanel
          entries={feed.entries}
          selfCity={selfRegion?.city ?? null}
          selfLocation={selfRegion?.location ?? null}
          selfAddress={selfInstance?.privateIP ?? null}
          servedAt={servedAt}
          compact={compact}
          reducedMotion={reducedMotion}
          limit={feedLimit}
          fill={fill}
        />
      </div>
    </Card>
  );

  return (
    <div className={`flex flex-col ${gap} ${stacked ? '' : 'h-full'}`}>
      <Card className={panelCard}>
        <div className={`grid ${wide ? 'grid-cols-4' : 'grid-cols-2'}`}>
          {tiles.map((tile, i) => (
            <Stat
              key={tile.label}
              {...tile}
              compact={compact}
              // Rules between the tiles, never around the outside of the card.
              className={rule(i, tiles.length, wide)}
            />
          ))}
        </div>
      </Card>

      {/* The two headline numbers share a card: the activity feed below needs
          the room, and they belong together anyway — all this traffic, none of
          it on the public internet. */}
      <Card className={`${panelCard} relative overflow-hidden`}>
        <div className="pointer-events-none absolute -top-20 -right-20 size-52 rounded-full bg-[#E6F59E]/[0.07] blur-3xl" />
        <div className="relative grid grid-cols-2">
          <div className={`${headline} border-r border-white/[0.07]`}>
            <PanelHeading>{compact ? 'Messages' : 'Messages exchanged'}</PanelHeading>
            <LiveCounter
              value={totals?.messages ?? 0}
              className={`mt-1.5 block leading-none font-medium tracking-[-0.03em] text-white tabular-nums ${
                compact ? 'text-[26px]' : 'text-[34px]'
              }`}
            />
            <p className={`text-white/45 ${compact ? 'mt-1 text-[11px]' : 'mt-1.5 text-[12px]'}`}>
              sent between Instances, privately
            </p>
          </div>
          <div className={headline} data-tour="public-internet">
            <PanelHeading>Public internet</PanelHeading>
            <div
              className={`mt-1.5 leading-none font-medium tracking-[-0.03em] text-[#E6F59E] tabular-nums ${
                compact ? 'text-[26px]' : 'text-[34px]'
              }`}
            >
              {totals?.publicInternetBytes ?? 0} bytes
            </div>
            <p className={`text-white/45 ${compact ? 'mt-1 text-[11px]' : 'mt-1.5 text-[12px]'}`}>
              from the whole fleet
            </p>
          </div>
        </div>
      </Card>

      {/* Activity before traffic: the platform reacting is the headline, and
          the traffic beside it is the proof it kept working through it. Two
          columns put them side by side; one keeps them in order, as direct
          children of the column so each still takes a share of its height. */}
      {wide ? (
        <div ref={feeds} className={`grid grid-cols-2 ${gap}`} style={feedsHeight ? { height: feedsHeight } : undefined}>
          {activityCard}
          {trafficCard}
        </div>
      ) : (
        <>
          {activityCard}
          {trafficCard}
        </>
      )}
    </div>
  );
}

/**
 * Which edges of a stat tile carry a rule, so the grid is divided inside and
 * clean around the outside whichever shape it is in.
 */
function rule(i: number, count: number, wide: boolean): string {
  const line = 'border-white/[0.07]';
  if (wide) return i < count - 1 ? `border-r ${line}` : '';
  return `${i % 2 === 0 ? `border-r ${line}` : ''} ${i < count - 2 ? `border-b ${line}` : ''}`;
}

function Stat({
  label,
  value,
  unit,
  accent,
  tour,
  compact = false,
  animate = true,
  className = '',
}: {
  label: string;
  value: number | string;
  unit?: string;
  accent?: string;
  /** Names the tile so a walkthrough card can point at it, or keep off it. */
  tour?: string;
  compact?: boolean;
  animate?: boolean;
  className?: string;
}) {
  const size = compact ? 'text-[26px]' : 'text-[32px]';
  return (
    <div data-tour={tour} className={`${compact ? 'px-4 py-3' : 'px-5 py-3.5'} ${className}`}>
      <PanelHeading>{label}</PanelHeading>
      <div className="mt-1.5 flex items-baseline gap-1.5">
        {animate ? (
          <AnimatePresence mode="popLayout" initial={false}>
            <motion.span
              key={String(value)}
              className={`leading-none font-medium tracking-[-0.03em] tabular-nums ${size}`}
              style={{ color: accent ?? '#ffffff' }}
              initial={{ opacity: 0, y: 10 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: -10, transition: { duration: 0.18 } }}
              transition={{ duration: 0.35, ease: EASE_OUT }}
            >
              {value}
            </motion.span>
          </AnimatePresence>
        ) : (
          <span
            className={`leading-none font-medium tracking-[-0.03em] tabular-nums ${size}`}
            style={{ color: accent ?? '#ffffff' }}
          >
            {value}
          </span>
        )}
        {unit && <span className={`text-white/50 ${compact ? 'text-[13px]' : 'text-[16px]'}`}>{unit}</span>}
      </div>
    </div>
  );
}
