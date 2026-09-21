import { useEffect, useRef, useState } from 'react';
import type { MeshView } from './types';

export interface MeshState {
  view: MeshView | null;
  /** performance.now() when the current view arrived. */
  receivedAt: number;
  /** True when the latest poll failed and an older view is shown. */
  stale: boolean;
}

/** Polls /api/mesh, keeping the last good view through transient failures. */
export function useMesh(intervalMs = 2000): MeshState {
  const [state, setState] = useState<MeshState>({ view: null, receivedAt: 0, stale: false });
  const inflight = useRef(false);

  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      // A backgrounded tab on a phone should not keep polling.
      if (inflight.current || document.hidden) return;
      inflight.current = true;
      try {
        const res = await fetch('/api/mesh', { cache: 'no-store' });
        if (!res.ok) throw new Error(res.statusText);
        const view = (await res.json()) as MeshView;
        if (!cancelled) setState({ view, receivedAt: performance.now(), stale: false });
      } catch {
        if (!cancelled) setState((s) => ({ ...s, stale: true }));
      } finally {
        inflight.current = false;
      }
    };
    void poll();
    const id = window.setInterval(poll, intervalMs);
    // Refresh as soon as the visitor comes back to the page.
    const onVisible = () => {
      if (!document.hidden) void poll();
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      cancelled = true;
      window.clearInterval(id);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [intervalMs]);

  return state;
}

/** Re-renders on an interval; used for ticking uptimes. */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => performance.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(performance.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
  return now;
}
