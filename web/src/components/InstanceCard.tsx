import { Badge } from '@datum-cloud/datum-ui/badge';
import { Card } from '@datum-cloud/datum-ui/card';
import { motion } from 'motion/react';
import { formatUptime } from '../mesh';
import type { Region } from '../types';
import { useNow } from '../useMesh';

interface Props {
  region: Region;
  /** Pin position in pixels; only used by the anchored variant. */
  at?: [number, number];
  mapWidth?: number;
  mapHeight?: number;
  /**
   * anchored: floats beside the pin, for the desktop map.
   * inline: sits in the flow below the map, for screens too small to cover.
   */
  variant?: 'anchored' | 'inline';
  /** How far the anchored card stands off its pin, clearing any replicas. */
  clearance?: number;
  compact?: boolean;
  receivedAt: number;
  onClose: () => void;
}

const CARD_WIDTH = 320;

export function InstanceCard({
  region,
  at,
  mapWidth = 0,
  mapHeight = 0,
  variant = 'anchored',
  clearance = 28,
  compact = false,
  receivedAt,
  onClose,
}: Props) {
  const now = useNow(1000);
  const elapsed = Math.max(0, (now - receivedAt) / 1000);

  const inline = variant === 'inline';
  const [x, y] = at ?? [0, 0];
  const flipX = x + CARD_WIDTH + clearance + 12 > mapWidth;
  const flipY = y > mapHeight * 0.55;

  return (
    <motion.div
      data-instance-card
      className={inline ? 'w-full' : 'absolute z-20'}
      style={
        inline
          ? undefined
          : {
              left: flipX ? x - CARD_WIDTH - clearance : x + clearance,
              top: y,
              width: CARD_WIDTH,
              translateY: flipY ? '-100%' : '-12%',
            }
      }
      initial={{ opacity: 0, y: 8, scale: inline ? 1 : 0.97 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      exit={{ opacity: 0, y: 6, scale: inline ? 1 : 0.98, transition: { duration: 0.18 } }}
      transition={{ duration: 0.32, ease: [0.22, 1, 0.36, 1] }}
      onClick={(e) => e.stopPropagation()}
    >
      <Card className="gap-0 overflow-hidden rounded-2xl border-white/10 bg-[#0f2340]/92 py-0 text-white shadow-[0_24px_80px_rgba(0,0,0,0.5)]">
        <div className="flex items-start justify-between gap-3 border-b border-white/[0.07] px-5 pt-4 pb-3.5">
          <div>
            <div className="flex items-center gap-2">
              <h3
                className={`leading-tight font-medium tracking-[-0.01em] ${compact ? 'text-[17px]' : 'text-[19px]'}`}
              >
                {region.city}
              </h3>
              {region.isSelf && (
                <span className="rounded-full bg-[#E6F59E] px-1.5 py-px text-[9.5px] font-semibold tracking-[0.12em] text-[#0c1d31] uppercase">
                  You
                </span>
              )}
            </div>
            <p className="mt-0.5 text-[13px] text-white/55">
              {region.country}
              <span className="text-white/35"> · {region.location}</span>
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="-mr-2 grid size-11 place-items-center rounded-md text-white/40 transition-colors hover:bg-white/5 hover:text-white/80"
          >
            <svg viewBox="0 0 16 16" className="size-4" fill="none" stroke="currentColor" strokeWidth="1.6">
              <path d="M4 4l8 8M12 4l-8 8" strokeLinecap="round" />
            </svg>
          </button>
        </div>

        {/* A location running several replicas lists them all, so the card
            scrolls rather than running off the map. */}
        <div className={`divide-y divide-white/[0.07] ${inline ? '' : 'max-h-[52vh] overflow-y-auto overscroll-contain'}`}>
          {region.instances.map((inst) => {
            const reachableShare = inst.peersTotal > 0 ? inst.peersReachable / inst.peersTotal : 1;
            const unreachable = !inst.reporting && inst.peersTotal > 0 && inst.peersReachable === 0;
            const status = unreachable
              ? { label: 'Unreachable', type: 'danger' as const }
              : inst.status === 'stopping'
                ? { label: 'Draining', type: 'muted' as const }
                : inst.status === 'running'
                  ? { label: 'Running', type: 'success' as const }
                  : { label: 'Starting', type: 'warning' as const };
            return (
              <div key={inst.name} className="space-y-3 px-5 py-4">
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate font-mono text-[11px] text-white/45" title={inst.name}>
                    {inst.name}
                  </span>
                  <Badge type={status.type} theme="light" className="shrink-0 rounded-full text-[11px]">
                    {status.label}
                  </Badge>
                </div>
                <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-[13px]">
                  <dt className="text-white/45">Private address</dt>
                  <dd className="truncate text-right font-mono text-[12px] text-white/90">{inst.privateIP || '—'}</dd>
                  <dt className="text-white/45">Uptime</dt>
                  <dd className="text-right text-white/90 tabular-nums">
                    {inst.uptimeSeconds > 0 || inst.reporting ? formatUptime(inst.uptimeSeconds + elapsed) : '—'}
                  </dd>
                  {inst.joinMs ? (
                    <>
                      <dt className="text-white/45">Joined the network in</dt>
                      <dd className="text-right text-[#E6F59E] tabular-nums">{(inst.joinMs / 1000).toFixed(1)}s</dd>
                    </>
                  ) : null}
                  <dt className="text-white/45">Peers reachable</dt>
                  <dd className="text-right text-white/90 tabular-nums">
                    {inst.peersTotal === 0 ? 'No peers yet' : `${inst.peersReachable} of ${inst.peersTotal}`}
                  </dd>
                </dl>
                {inst.peersTotal > 0 && (
                  <div className="h-1 overflow-hidden rounded-full bg-white/[0.08]">
                    <motion.div
                      className="h-full rounded-full"
                      style={{ background: reachableShare === 1 ? '#B3D56F' : reachableShare === 0 ? '#EF7B6C' : '#F2C46D' }}
                      initial={{ width: 0 }}
                      animate={{ width: `${reachableShare * 100}%` }}
                      transition={{ duration: 0.8, ease: [0.22, 1, 0.36, 1] }}
                    />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </Card>
    </motion.div>
  );
}
