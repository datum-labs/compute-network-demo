import { useEffect } from 'react';
import type { Box } from './labels';
import { formatMs } from './mesh';
import type { IntraLink, Link, MeshView, Region } from './types';

/**
 * The vocabulary the two halves of the walkthrough share.
 *
 * The demo explains itself in two phases, and they want opposite controls:
 *
 * - The **intro** (intro.ts) is a short fixed sequence of hand-written cards
 *   captioning a picture that is not changing. Nothing is happening, so the
 *   visitor may page through it: Back, Next and a progress ring.
 * - The **narration** (narration.ts) takes over when the intro finishes, and
 *   raises a card each time the workload actually does something. There is no
 *   paging through that — a scale-up cannot be rewound, and a card for one
 *   would describe a map that has moved on.
 *
 * Keeping them in separate modules keeps the seam obvious: narration reads
 * only what the fleet view reports, so the day those events come from a real
 * workload rather than a simulated one, nothing in the intro has to change.
 *
 * Neither half ever commands the fleet. One process serves every viewer, so a
 * cue from one browser would move the world for everyone else watching.
 */

/** Where a card wants the camera. */
export type StoryShot =
  | { at: 'fleet' }
  /** closeness is a fraction of the whole-fleet framing; smaller is closer. */
  | { at: 'location'; location: string; closeness: number };

/** What a card is talking about, so it can sit beside it and not on it. */
export type StoryTarget =
  | { at: 'fleet' }
  | { at: 'location'; location: string }
  /** Something outside the map, found by its data-tour name. */
  | { at: 'panel'; name: 'activity' | 'public-internet' };

/** One card: a heading and a sentence or two, as a product tour reads. */
export interface Card {
  title: string;
  body: string;
}

/** Everything the cards read, handed in from the page. */
export interface StoryInputs {
  view: MeshView | null;
  regions: Region[];
  links: Link[];
  intraLinks: IntraLink[];
}

/** What is on screen, whichever half of the walkthrough put it there. */
export interface Showing {
  /** null between narration cards, when the camera is steered but nothing is said. */
  card: Card | null;
  shot: StoryShot | null;
  target: StoryTarget;
  /** The location to spotlight, so the map agrees with the card. */
  focus: string | null;
  /**
   * Which card is on screen. Live numbers inside a card tick in place; only a
   * new card is worth animating.
   */
  beat: string;
  /** Bumped whenever the camera should be sent to the shot again. */
  flight: number;
}

/**
 * Where the map has put things, in viewport pixels, so a card floating over it
 * can dodge what is already drawn. The map reports this rather than the page
 * guessing: the same layout pass that places the city names knows exactly what
 * is occupied.
 */
export interface Stage {
  /** The map surface itself, which bounds anything placed over it. */
  surface: Box;
  /** Pins, city names and arc labels the map has already claimed. */
  reserved: Box[];
  /** Each location's pin with the Instances fanned around it. */
  subjects: Record<string, Box>;
  /** The whole fleet's extent, for a card that is about all of it. */
  fleet: Box;
}

/** How close a step flies when its subject is one location's Instances. */
export const CLOSE_UP = 0.12;

/** "Dallas, Ashburn and San Jose" — the fleet read out as a sentence. */
export function cityList(regions: Region[]): string {
  const cities = regions.map((r) => r.city);
  if (cities.length === 0) return 'every region';
  if (cities.length === 1) return cities[0];
  return `${cities.slice(0, -1).join(', ')} and ${cities[cities.length - 1]}`;
}

/** The pair the private network has to work hardest for. */
export function farthestLink(links: Link[]): Link | null {
  return links
    .filter((l) => l.rttMs > 0 && l.state !== 'pending')
    .reduce<Link | null>((far, l) => (!far || l.rttMs > far.rttMs ? l : far), null);
}

/** The location worth flying into: the one that served the page, or the busiest. */
export function headline(regions: Region[]): Region | null {
  if (regions.length === 0) return null;
  return (
    regions.find((r) => r.isSelf) ??
    regions.reduce((most, r) => (r.instances.length > most.instances.length ? r : most), regions[0])
  );
}

export function regionOf(regions: Region[], location: string | null | undefined): Region | null {
  return regions.find((r) => r.location === location) ?? null;
}

/** Instances a location is scaled to, which excludes the one on its way out. */
/**
 * How the fleet is spread, city by city: "4 in Dallas, 3 in Ashburn and 2 in
 * San Jose". It is what the badges on the pins say, put into words, biggest
 * first so the sentence opens on the busiest place.
 */
export function instanceSpread(regions: Region[]): string {
  const parts = regions
    .filter((r) => liveCount(r) > 0)
    .sort((a, b) => liveCount(b) - liveCount(a))
    .map((r) => `${liveCount(r)} in ${r.city}`);
  if (parts.length === 0) return 'nothing yet';
  if (parts.length === 1) return parts[0];
  return `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}`;
}

export function liveCount(region: Region | null): number {
  return region ? region.instances.filter((i) => i.status !== 'stopping').length : 0;
}

/** How far apart a location's own Instances are, averaged over its links. */
export function intraApart(intraLinks: IntraLink[], location: string | null): string | null {
  const rtts = intraLinks.filter((l) => l.location === location && l.rttMs > 0).map((l) => l.rttMs);
  if (rtts.length === 0) return null;
  return formatMs(rtts.reduce((sum, v) => sum + v, 0) / rtts.length);
}

/** Kilometres between two points on the globe. */
function kmBetween(a: Region, b: Region): number {
  const toRad = (d: number) => (d * Math.PI) / 180;
  const dLat = toRad(b.lat - a.lat);
  const dLon = toRad(b.lon - a.lon);
  const h =
    Math.sin(dLat / 2) ** 2 + Math.cos(toRad(a.lat)) * Math.cos(toRad(b.lat)) * Math.sin(dLon / 2) ** 2;
  return 6371 * 2 * Math.asin(Math.min(1, Math.sqrt(h)));
}

/** How far a location's most distant peer is, rounded to something sayable. */
export function farthestPeer(regions: Region[], from: Region | null): string | null {
  if (!from) return null;
  const km = regions.filter((r) => r !== from).reduce((far, r) => Math.max(far, kmBetween(from, r)), 0);
  if (km < 100) return null;
  return `${count(Math.round(km / 100) * 100)} km`;
}

export const seconds = (ms: number) => `${(ms / 1000).toFixed(1)}s`;
export const count = (n: number) => n.toLocaleString('en-US');

/**
 * Any sign of the visitor steering hands the camera over. The words carry on:
 * the walkthrough describes the workload, not the view, and taking the whole
 * thing away because someone leaned in is the opposite of what they asked for.
 * The intro also stops its clock, so a card is never pulled out from under
 * someone who just reached for the map.
 */
export function useStoryInterrupt(showing: boolean, handOver: () => void) {
  useEffect(() => {
    if (!showing) return;
    const hand = (e: Event) => {
      // The walkthrough's own card is not the visitor steering.
      if ((e.target as Element | null)?.closest?.('[data-story-control]')) return;
      handOver();
    };
    const kinds = ['wheel', 'pointerdown', 'touchstart'] as const;
    for (const kind of kinds) window.addEventListener(kind, hand, { capture: true, passive: true });
    return () => {
      for (const kind of kinds) window.removeEventListener(kind, hand, { capture: true });
    };
  }, [showing, handOver]);
}

/**
 * How the walkthrough behaves on this visit, from `?story=`.
 *
 * Narration is not an interruption to be switched off with the intro — it is
 * the demo, and a card only appears when the workload has done something worth
 * a sentence. So `off` silences the intro and leaves the narration running,
 * and `quiet` is the setting for a still page and for screenshots.
 */
export function storySettings(search: string): { intro: boolean; loop: boolean; narrate: boolean } {
  switch (new URLSearchParams(search).get('story')) {
    // A booth screen wants the explanation on repeat, so the intro never ends
    // and never hands over.
    case 'loop':
      return { intro: true, loop: true, narrate: false };
    case 'off':
      return { intro: false, loop: false, narrate: true };
    case 'quiet':
      return { intro: false, loop: false, narrate: false };
    default:
      return { intro: true, loop: false, narrate: true };
  }
}
