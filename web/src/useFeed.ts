import { useEffect, useRef, useState } from 'react';
import { intraLinkKey } from './mesh';
import type { Exchange, InstanceView, MeshView } from './types';

export interface FeedEntry extends Exchange {
  key: string;
  /** Link key shared with the map, so the matching arc can be flashed. */
  linkKey: string | null;
  /** How each end is named in the feed: its city, and its ordinal if it shares one. */
  fromCity: string;
  toCity: string;
}

/**
 * A poll arrives every couple of seconds carrying every exchange that happened
 * in between, which with eight instances is far more than anyone can read.
 * Lines are let out one at a time, oldest first, so the feed ticks along with
 * the traffic on the map instead of arriving in clumps.
 *
 * About three lines a second: fast enough to feel live, slow enough to read.
 */
const RELEASE_EVERY_MS = 300;
/**
 * How deep the feed's own memory goes. A tall column shows around two dozen
 * lines, which at this release rate is the last seven seconds or so of the
 * fleet's traffic — still a live feed rather than a log.
 */
const MAX_ENTRIES = 26;
/** How long an arc stays lit after its exchange appears in the feed. */
const FLASH_MS = 850;
/**
 * At most this many arcs are lit at once. A busy fleet exchanges messages
 * faster than the eye can follow, and lighting every arc would just mean the
 * whole map glows.
 */
const MAX_FLASHING = 3;
/**
 * Older backlog is dropped rather than shown late. Eight instances exchange
 * messages several times faster than a readable feed can show them, so the
 * feed is a sample of the newest traffic rather than a queue of all of it.
 */
const MAX_PENDING = 12;
/**
 * How many of the last lines' pairs a new line avoids repeating. Every
 * Instance exchanges with every other one each round, so the same handful of
 * pairs is always waiting; without this they march down the feed in the same
 * order, which a long list makes obvious and a short one hid.
 */
const RECENT_LINKS = 4;

const entryKey = (e: Exchange) => `${e.at}|${e.from}|${e.to}`;

function linkKeyFor(view: MeshView, from: string, to: string): string | null {
  const locationOf = new Map(view.instances.map((i) => [i.name, i.location]));
  const a = locationOf.get(from);
  const b = locationOf.get(to);
  if (!a || !b) return null;
  // Replicas in one location have their own short link, drawn once the map is
  // zoomed in far enough to show them apart.
  if (a === b) return intraLinkKey(from, to);
  return a < b ? `${a}|${b}` : `${b}|${a}`;
}

/**
 * How the feed names each instance. A location running several replicas needs
 * the ordinal to tell one line from the next; a single instance reads better
 * as just its city.
 */
function feedNames(instances: InstanceView[]): Map<string, string> {
  const byLocation = new Map<string, InstanceView[]>();
  for (const inst of instances) {
    byLocation.set(inst.location, [...(byLocation.get(inst.location) ?? []), inst]);
  }
  const names = new Map<string, string>();
  for (const group of byLocation.values()) {
    const ordered = [...group].sort((a, b) => a.name.localeCompare(b.name));
    ordered.forEach((inst, i) => {
      const city = inst.city || inst.location;
      names.set(inst.name, ordered.length > 1 ? `${city} ${i + 1}` : city);
    });
  }
  return names;
}

export interface Feed {
  entries: FeedEntry[];
  /** Link keys whose arc should be lit right now. */
  flashing: Set<string>;
}

export function useFeed(view: MeshView | null, enabled = true): Feed {
  const [entries, setEntries] = useState<FeedEntry[]>([]);
  const [flashing, setFlashing] = useState<Set<string>>(() => new Set());
  const pending = useRef<FeedEntry[]>([]);
  const seen = useRef<Set<string>>(new Set());
  const flashUntil = useRef<Map<string, number>>(new Map());
  const recentLinks = useRef<string[]>([]);

  useEffect(() => {
    if (!view || !enabled) return;
    const cityOf = feedNames(view.instances);
    // Oldest first, so they are released in the order they happened.
    for (const e of [...view.exchanges].reverse()) {
      const key = entryKey(e);
      if (seen.current.has(key)) continue;
      seen.current.add(key);
      pending.current.push({
        ...e,
        key,
        linkKey: linkKeyFor(view, e.from, e.to),
        fromCity: cityOf.get(e.from) ?? e.from,
        toCity: cityOf.get(e.to) ?? e.to,
      });
    }
    // Keep the queue short: a feed that runs behind is not a live feed.
    if (pending.current.length > MAX_PENDING) {
      pending.current = pending.current.slice(-MAX_PENDING);
    }
    // The set of keys is unbounded otherwise.
    if (seen.current.size > 400) {
      seen.current = new Set(pending.current.map((p) => p.key));
    }
  }, [view, enabled]);

  useEffect(() => {
    if (!enabled) return;
    const id = window.setInterval(() => {
      // A backgrounded tab has nobody reading the feed, and the queue keeps
      // only the newest traffic anyway, so coming back picks up from now.
      if (document.hidden) return;
      const now = Date.now();
      // One line per tick, and never a pair shown in the last few: with every
      // instance talking to every other, the same arcs would otherwise fill
      // the feed in the same order before any other got a turn.
      const queue = pending.current;
      const e = queue.find((p) => !p.linkKey || !recentLinks.current.includes(p.linkKey)) ?? queue[0];

      if (e) {
        pending.current = queue.filter((p) => p !== e);
        if (e.linkKey) recentLinks.current = [e.linkKey, ...recentLinks.current].slice(0, RECENT_LINKS);
        setEntries((current) => [e, ...current].slice(0, MAX_ENTRIES));
        if (e.linkKey && e.ok) {
          if (flashUntil.current.size >= MAX_FLASHING && !flashUntil.current.has(e.linkKey)) {
            // Make room by dropping whichever arc has been lit longest.
            const oldest = [...flashUntil.current.entries()].sort((a, b) => a[1] - b[1])[0];
            flashUntil.current.delete(oldest[0]);
          }
          flashUntil.current.set(e.linkKey, now + FLASH_MS);
        }
      }

      const lit = new Set<string>();
      for (const [key, until] of flashUntil.current) {
        if (until > now) lit.add(key);
        else flashUntil.current.delete(key);
      }
      setFlashing((prev) => (sameSet(prev, lit) ? prev : lit));
    }, RELEASE_EVERY_MS);
    return () => window.clearInterval(id);
  }, [enabled]);

  return { entries, flashing };
}

function sameSet(a: Set<string>, b: Set<string>): boolean {
  if (a.size !== b.size) return false;
  for (const v of a) if (!b.has(v)) return false;
  return true;
}
