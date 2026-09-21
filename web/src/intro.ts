import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { formatMs } from './mesh';
import {
  CLOSE_UP,
  type Card,
  type Showing,
  type StoryInputs,
  type StoryShot,
  type StoryTarget,
  cityList,
  count,
  farthestLink,
  farthestPeer,
  headline,
  instanceSpread,
  intraApart,
  liveCount,
} from './story';

/**
 * The intro: four hand-written cards that say what the visitor is looking at.
 *
 * Nothing is happening while they show — the map is a picture and these are
 * its captions — so the visitor may page through them. That is the whole
 * reason this half has Back and Next and the narration half does not.
 *
 * The words are written here; the numbers in them are read off the fleet as
 * the card shows, so the copy and the screen cannot drift apart. The cards
 * also carry the camera: the sequence opens on the whole fleet and ends inside
 * one location, close enough that its Instances fan out and can be counted.
 */

interface Chapter {
  id: string;
  card: (s: StoryInputs) => Card;
  shot: (s: StoryInputs) => StoryShot;
  target: (s: StoryInputs) => StoryTarget;
  /** The location to spotlight, so the arcs and labels agree with the words. */
  focus?: (s: StoryInputs) => string | null;
  /** How long the card holds before it advances on its own. */
  dwellMs: number;
}

export const CHAPTERS: Chapter[] = [
  {
    id: 'deployed-once',
    card: ({ view, regions }) => ({
      title: 'Deploy global workloads securely',
      body: `One workload definition, deployed once, is now ${view?.totals.instances ?? 0} Instances across ${cityList(regions)} — and each one is reachable only by the rest of the fleet.`,
    }),
    shot: () => ({ at: 'fleet' }),
    target: () => ({ at: 'fleet' }),
    dwellMs: 12000,
  },
  {
    id: 'every-region',
    card: ({ regions }) => ({
      title: 'Let Datum choose where it runs',
      body: `Datum placed ${instanceSpread(regions)}. The badge on each pin is what it decided to run there, and it moves as demand does.`,
    }),
    shot: () => ({ at: 'fleet' }),
    target: () => ({ at: 'fleet' }),
    dwellMs: 14000,
  },
  {
    id: 'private-network',
    card: ({ links, view }) => {
      const far = farthestLink(links);
      const pair = far
        ? `${far.a.city} reaches ${far.b.city} in ${formatMs(far.rttMs)}`
        : 'Every Instance can reach every other one';
      return {
        title: 'Reach every region privately',
        // The arcs are the measurement, so the sentence names the one the map
        // is lighting up and the bytes figure the panel is counting.
        body: `${pair}, and the fleet has sent ${count(view?.totals.publicInternetBytes ?? 0)} bytes over the public internet. Datum set that private network up when the workload deployed.`,
      };
    },
    shot: () => ({ at: 'fleet' }),
    target: ({ links }) => {
      const far = farthestLink(links);
      return far ? { at: 'location', location: far.a.location } : { at: 'fleet' };
    },
    focus: ({ links }) => farthestLink(links)?.a.location ?? null,
    dwellMs: 15000,
  },
  {
    id: 'inside-a-location',
    card: ({ regions, intraLinks }) => {
      const here = headline(regions);
      const apart = intraApart(intraLinks, here?.location ?? null);
      const away = farthestPeer(regions, here);
      const n = liveCount(here);
      return {
        title: 'Scale up on the same network',
        body: `${here?.city ?? 'This location'} is running ${n} Instance${n === 1 ? '' : 's'}${apart ? `, about ${apart} apart` : ''}. Each one joined the same private network as the Instances ${away ?? 'a continent'} away as it started.`,
      };
    },
    // The one card that flies right in, so the range between a whole fleet and
    // a single Instance is on screen rather than described.
    shot: ({ regions }) => {
      const here = headline(regions);
      return here ? { at: 'location', location: here.location, closeness: CLOSE_UP } : { at: 'fleet' };
    },
    target: ({ regions }) => {
      const here = headline(regions);
      return here ? { at: 'location', location: here.location } : { at: 'fleet' };
    },
    focus: ({ regions }) => headline(regions)?.location ?? null,
    dwellMs: 16000,
  },
];

/** The intro, for the card, the camera and the spotlight. */
export interface Intro extends Showing {
  playing: boolean;
  index: number;
  count: number;
  paused: boolean;
  /** True once the intro has run to the end, so narration may take over. */
  finished: boolean;
  /**
   * True once the intro has been opened at all. A page where it never opened
   * by itself offers to play it rather than to play it again.
   */
  played: boolean;
  /** performance.now bounds of the card on screen, for the progress ring. */
  startedAt: number;
  endsAt: number;
  /** When the clock stopped, so the ring holds rather than filling unseen. */
  pausedAt: number | null;
  start: (from?: number) => void;
  back: () => void;
  next: () => void;
  pause: () => void;
  resume: () => void;
  stop: () => void;
}

interface Playhead {
  index: number;
  openedAt: number;
  pausedAt: number | null;
  flight: number;
}

/** Reduced motion keeps the words and loses the flying, not the reading time. */
const REDUCED_PACE = 0.95;

/** How often the playhead is checked. Cards turn over in seconds. */
const TICK_MS = 250;

/** How long after the fleet first appears the intro opens, unasked. */
const AUTOPLAY_DELAY_MS = 1600;

/**
 * How long a paused intro waits before it carries on by itself. Someone who
 * zoomed in and walked away should not leave the next visitor with a card that
 * never moves; this lines up with the map handing its own camera back.
 */
const RESUME_AFTER_IDLE_MS = 20000;

export function useIntro(
  inputs: StoryInputs,
  {
    autoplay,
    loop,
    reducedMotion,
  }: {
    /**
     * Whether the intro opens by itself, or null while the page does not yet
     * know — the map has to have drawn before it can say whether it has room
     * to show a card.
     */
    autoplay: boolean | null;
    loop: boolean;
    reducedMotion: boolean;
  }
): Intro {
  const [head, setHead] = useState<Playhead | null>(null);
  const [finished, setFinished] = useState(autoplay === false);
  // The ticker reads the fleet through refs, so a poll arriving does not tear
  // down and rebuild the interval the intro is running on.
  const headRef = useRef<Playhead | null>(null);
  headRef.current = head;
  const loopRef = useRef(loop);
  loopRef.current = loop;
  const pace = reducedMotion ? REDUCED_PACE : 1;
  const paceRef = useRef(pace);
  paceRef.current = pace;

  const open = useCallback(
    (index: number, from: Playhead | null): Playhead => ({
      index,
      openedAt: performance.now(),
      // Paging by hand is the visitor setting their own speed, so the clock
      // stays where they left it rather than restarting behind them.
      pausedAt: from?.pausedAt != null ? performance.now() : null,
      flight: (from?.flight ?? 0) + 1,
    }),
    []
  );

  const start = useCallback(
    (from = 0) => {
      setFinished(false);
      setPlayed(true);
      setHead(open(from, null));
    },
    [open]
  );
  const stop = useCallback(() => {
    setHead(null);
    setFinished(true);
  }, []);
  // Every fresh sign of the visitor restarts the idle clock, and the card's
  // own clock is wound on by however long the pause has run so far, so the
  // progress ring holds exactly where it stopped.
  const pause = useCallback(
    () =>
      setHead((h) => {
        if (!h || loopRef.current) return h;
        const now = performance.now();
        return h.pausedAt == null ? { ...h, pausedAt: now } : { ...h, openedAt: h.openedAt + (now - h.pausedAt), pausedAt: now };
      }),
    []
  );
  const resume = useCallback(
    () =>
      setHead((h) =>
        h && h.pausedAt != null
          ? { ...h, openedAt: h.openedAt + (performance.now() - h.pausedAt), pausedAt: null, flight: h.flight + 1 }
          : h
      ),
    []
  );
  // Paging pauses rather than cancels: someone who reached for Back wants to
  // read, not to be moved on four seconds later.
  const go = useCallback(
    (delta: number) =>
      setHead((h) => {
        if (!h) return h;
        const index = (h.index + delta + CHAPTERS.length) % CHAPTERS.length;
        const paged = open(index, h);
        return loopRef.current ? paged : { ...paged, pausedAt: performance.now() };
      }),
    [open]
  );
  const back = useCallback(() => go(-1), [go]);
  const next = useCallback(() => go(1), [go]);

  // Autoplay waits for the fleet: opening on an empty map would spend the
  // first card saying something the screen cannot back up yet.
  //
  // It is also decided exactly once, on arrival. A page that had nowhere to
  // put a card when it loaded does not start one later because the phone was
  // turned, and a page that has been read for five minutes does not suddenly
  // open a tour at whoever is reading it. Turning a phone is not arriving.
  const ready = !!inputs.view;
  const [played, setPlayed] = useState(false);
  const decided = useRef(false);
  useEffect(() => {
    if (autoplay === null || !ready || decided.current) return;
    decided.current = true;
    if (!autoplay) {
      // Nothing is going to say the opening words, so narration may have the
      // floor from the start.
      setFinished(true);
      return;
    }
    const id = window.setTimeout(() => start(), AUTOPLAY_DELAY_MS);
    return () => window.clearTimeout(id);
  }, [autoplay, ready, start]);

  useEffect(() => {
    if (!head) return;
    const id = window.setInterval(() => {
      const current = headRef.current;
      if (current?.pausedAt != null) {
        if (performance.now() - current.pausedAt >= RESUME_AFTER_IDLE_MS) resume();
        return;
      }
      if (!current) return;
      if (performance.now() < current.openedAt + CHAPTERS[current.index].dwellMs * paceRef.current) return;
      const following = current.index + 1;
      if (following < CHAPTERS.length) {
        setHead(open(following, current));
        return;
      }
      if (loopRef.current) {
        setHead(open(0, current));
        return;
      }
      setHead(null);
      setFinished(true);
    }, TICK_MS);
    return () => window.clearInterval(id);
    // Only the presence of a playhead matters; its contents are read per tick.
  }, [!!head, open, resume]); // eslint-disable-line react-hooks/exhaustive-deps

  const chapter = head ? CHAPTERS[head.index] : null;
  const card = useMemo(() => (chapter ? chapter.card(inputs) : { title: '', body: '' }), [chapter, inputs]);

  return {
    playing: !!head,
    index: head?.index ?? 0,
    count: CHAPTERS.length,
    finished,
    played,
    card,
    shot: chapter ? chapter.shot(inputs) : null,
    target: chapter ? chapter.target(inputs) : { at: 'fleet' },
    focus: chapter?.focus?.(inputs) ?? null,
    beat: head ? `intro:${head.index}` : '',
    paused: head?.pausedAt != null,
    startedAt: head?.openedAt ?? 0,
    endsAt: head && chapter ? head.openedAt + chapter.dwellMs * pace : 0,
    pausedAt: head?.pausedAt ?? null,
    flight: head?.flight ?? 0,
    start,
    back,
    next,
    pause,
    resume,
    stop,
  };
}
