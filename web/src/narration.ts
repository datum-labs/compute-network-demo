import { useEffect, useRef, useState } from 'react';
import {
  CLOSE_UP,
  type Card,
  type Showing,
  type StoryInputs,
  cityList,
  regionOf,
  seconds,
} from './story';
import type { Activity } from './types';

/**
 * Live narration: a card each time the workload does something.
 *
 * It takes over when the intro finishes and then stays for as long as the page
 * is open. There is no paging through it — a scale-up cannot be rewound, and a
 * card offering to would describe a map that has already moved on. The missing
 * Back and Next are the point: what is on screen happened, just now.
 *
 * Everything here is driven by the activity stream in the fleet view, which
 * the server builds by comparing one observation of the fleet with the last.
 * That is the seam: the same five event types arrive whether the fleet is
 * simulated or real, so a live workload needs nothing added here. Nothing in
 * this module reaches into the simulator, and nothing in it asks the fleet to
 * do anything — one process serves every viewer, so a cue from one browser
 * would move the world for everyone else watching.
 */

/** How each kind of event reads once it is the headline rather than a log line. */
function narrate(e: Activity, { regions }: StoryInputs): Card | null {
  const where = e.city || e.location;
  switch (e.type) {
    case 'scaled-up':
      return {
        title: `Demand is rising in ${where}`,
        body: `${where} is scaling itself to ${e.to} Instances. The rest of the fleet carries on exactly as it is.`,
      };
    case 'scaled-down':
      return {
        title: `Demand has eased in ${where}`,
        body: `${where} is back to ${e.to} Instances. Capacity follows the traffic down as readily as it follows it up.`,
      };
    case 'instance-ready':
      return {
        title: e.joinMs ? `A new Instance joined in ${seconds(e.joinMs)}` : 'A new Instance joined',
        body: `The Instance that just started in ${where} can already reach every peer in ${cityList(regions)}. Datum put it on the network as it came up.`,
      };
    case 'instance-stopping':
      return {
        title: `An Instance is winding down in ${where}`,
        body: 'Its traffic moves across to the others before it goes, and the rest of the fleet carries on.',
      };
    // An Instance appearing is only half a story; the card comes when it has
    // joined the network and there is a number to put on it.
    case 'instance-starting':
      return null;
  }
}

/** How close the camera flies for each kind of event. */
function closeness(e: Activity): number {
  // A join is about one Instance among its peers, so it goes right in; a
  // scaling decision is about the shape of a location, which reads from
  // further out with its neighbours still in frame.
  return e.type === 'instance-ready' ? CLOSE_UP : 0.2;
}

/** What is on screen now, plus when the event behind it happened. */
export interface Narration {
  /** Where the camera should be, and the card to put over it if there is one. */
  showing: Showing | null;
  /** Browser time of the event the card describes, for its "just now" line. */
  since: number;
  /** Dismisses narration for good; the visitor asked for a still page. */
  stop: () => void;
  restart: () => void;
}

/**
 * How each kind of event ranks when several are waiting. A burst is common —
 * a location scales, an Instance starts, and it joins — and the card that
 * makes the point is the one about the join.
 */
const RANK: Record<Activity['type'], number> = {
  'instance-ready': 4,
  'scaled-up': 3,
  'scaled-down': 2,
  'instance-stopping': 1,
  'instance-starting': 0,
};

/** How long one card holds before the map is handed back to itself. */
const DWELL_MS = 11000;

/**
 * Quiet between cards. The fleet a workload is spread across is the picture
 * this demo is selling, and a card every eleven seconds would keep the camera
 * inside one city for the whole visit.
 */
const GAP_MS = 6000;

/**
 * How long the same kind of event waits before it is worth saying again. Three
 * locations breathing in and out produce the same four sentences all day.
 */
const COOLDOWN_MS = 40000;

/** How often the activity stream is checked for something worth saying. */
const TICK_MS = 500;

/** Past this, an event is history rather than news and is not worth a card. */
const MAX_AGE_MS = 15000;

/** Waiting cards older than this are dropped rather than shown late. */
const QUEUE_LIMIT = 3;

/** Matches the key the activity feed uses, so both agree on what is new. */
const eventKey = (e: Activity) => `${e.at}|${e.type}|${e.instance ?? e.location}`;

interface Raised {
  event: Activity;
  /** Browser time the event happened, corrected for the fleet's own clock. */
  at: number;
  openedAt: number;
  flight: number;
}

export function useNarration(inputs: StoryInputs, { active }: { active: boolean }): Narration {
  const [raised, setRaised] = useState<Raised | null>(null);
  const [stopped, setStopped] = useState(false);
  const inputsRef = useRef(inputs);
  inputsRef.current = inputs;
  // Everything the page was already showing when narration took over is
  // history: the fleet view carries a couple of minutes of backfill so the
  // activity feed is never empty, and none of it just happened.
  const seen = useRef<Set<string> | null>(null);
  const queue = useRef<Activity[]>([]);
  const raisedRef = useRef<Raised | null>(null);
  raisedRef.current = raised;
  const told = useRef(new Map<string, number>());
  const quietUntil = useRef(0);
  const flight = useRef(0);

  const running = active && !stopped;
  useEffect(() => {
    if (!running) {
      seen.current = null;
      queue.current = [];
      told.current.clear();
      setRaised(null);
      return;
    }
    const id = window.setInterval(() => {
      const current = inputsRef.current;
      const activity = current.view?.activity ?? [];
      if (seen.current === null) {
        seen.current = new Set(activity.map(eventKey));
        return;
      }
      // Oldest first, so a burst is narrated in the order it happened.
      for (const e of [...activity].reverse()) {
        const key = eventKey(e);
        if (seen.current.has(key)) continue;
        seen.current.add(key);
        if (narrate(e, current)) queue.current.push(e);
      }
      queue.current = queue.current.slice(-QUEUE_LIMIT);

      const now = performance.now();
      const held = raisedRef.current;
      if (held && now - held.openedAt < DWELL_MS) return;
      if (held) {
        setRaised(null);
        quietUntil.current = now + GAP_MS;
        return;
      }
      if (now < quietUntil.current) return;

      const clock = fleetClock(current);
      // Anything that sat behind another card long enough to be history is
      // dropped rather than announced late, as is anything the page said only
      // a moment ago.
      const fresh = queue.current.filter(
        (e) => clock - Date.parse(e.at) <= MAX_AGE_MS && now - (told.current.get(e.type) ?? -Infinity) >= COOLDOWN_MS
      );
      const next = fresh.reduce<Activity | null>((best, e) => (!best || RANK[e.type] > RANK[best.type] ? e : best), null);
      queue.current = [];
      if (!next) return;
      told.current.set(next.type, now);
      flight.current += 1;
      setRaised({ event: next, at: Date.parse(next.at) + (Date.now() - clock), openedAt: now, flight: flight.current });
    }, TICK_MS);
    return () => window.clearInterval(id);
  }, [running]);

  const card = raised ? narrate(raised.event, inputs) : null;
  const region = raised ? regionOf(inputs.regions, raised.event.location) : null;

  return {
    showing: running
      ? card && raised
        ? {
            card,
            shot: region
              ? { at: 'location', location: region.location, closeness: closeness(raised.event) }
              : { at: 'fleet' },
            target: region ? { at: 'location', location: region.location } : { at: 'fleet' },
            focus: region?.location ?? null,
            beat: `narration:${eventKey(raised.event)}`,
            flight: raised.flight,
          }
        : // Between cards the map goes back to the whole fleet, so there is
          // always a framing that explains itself when nobody is watching.
          { card: null, shot: { at: 'fleet' }, target: { at: 'fleet' }, focus: null, beat: '', flight: 0 }
      : null,
    since: raised?.at ?? 0,
    stop: () => setStopped(true),
    restart: () => setStopped(false),
  };
}

/**
 * Browser time as the fleet would read it. Events are stamped by the fleet, so
 * a visitor whose machine is a minute out still sees "just now".
 */
function fleetClock({ view }: StoryInputs): number {
  const generated = view ? Date.parse(view.generatedAt) : NaN;
  return Number.isFinite(generated) ? generated : Date.now();
}
