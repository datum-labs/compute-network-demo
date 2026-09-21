import { AnimatePresence, motion } from 'motion/react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { type Box, CARD_CLEARANCE, type CardPlacement, placeCard, placeFleetCard } from '../labels';
import type { Card, Stage, StoryTarget } from '../story';
import { useNow } from '../useMesh';

const EASE_OUT = [0.22, 1, 0.36, 1] as const;

/**
 * Card sizes, the same two-step way the map's labels are sized in labels.ts:
 * one set for a projector and a laptop, a tighter one for a phone.
 *
 * The card is a caption over a live map, so every pixel it takes is a pixel of
 * the thing it is describing. It is held to what its words need and no more —
 * but this is read across a room, so the type stops a step above the size the
 * rest of the interface chrome uses rather than shrinking to match it.
 */
interface CardMetrics {
  width: number;
  /** What the card measures at its natural width, and once narrowed as far
   * as it will go. */
  height: number;
  smallestHeight: number;
  pad: string;
  title: string;
  body: string;
  /**
   * Room reserved for the sentence, so a live number ticking inside one cannot
   * reflow the card out from under its own pointer.
   */
  bodyMin: string;
  /** The gap above the buttons, and the buttons' own size. */
  controls: string;
  button: string;
}

const FULL_CARD: CardMetrics = {
  width: 336,
  height: 190,
  smallestHeight: 248,
  pad: 'px-5 py-4',
  title: 'text-[18px]',
  body: 'text-[13px]',
  bodyMin: 'min-h-[54px]',
  controls: 'mt-3',
  button: 'px-3.5 py-1.5 text-[12.5px]',
};

const COMPACT_CARD: CardMetrics = {
  width: 288,
  height: 196,
  smallestHeight: 196,
  pad: 'px-4 py-3.5',
  title: 'text-[16px]',
  body: 'text-[12.5px]',
  bodyMin: 'min-h-[50px]',
  controls: 'mt-2.5',
  button: 'px-3 py-1.5 text-[12px]',
};

/**
 * Whether a card can float on a map of this size clear of its fleet.
 *
 * A card needs a band to stand in: above the fleet or below it at its natural
 * height, or beside it at the narrowest width it will read at.
 */
export function cardFitsOnMap(fleet: Box, map: { width: number; height: number }, compact: boolean): boolean {
  const m = compact ? COMPACT_CARD : FULL_CARD;
  // The margin a card is placed with, plus the room it keeps around whatever
  // it lands next to.
  const spare = CARD_MARGIN + CARD_CLEARANCE;
  const beside = Math.max(fleet.x0, map.width - fleet.x1);
  return (
    fleet.y0 >= m.height + spare ||
    map.height - fleet.y1 >= m.height + spare ||
    (beside >= MIN_CARD_WIDTH + spare && map.height >= m.smallestHeight + 2 * spare)
  );
}

/**
 * Whether the walkthrough has anywhere at all to put a card on this layout:
 * a clear band inside the map, a stacked page that can grow to make room under
 * it, or a map with enough of itself to spare for a caption bar.
 *
 * Where the answer is no, every spot is on top of the fleet, and the
 * walkthrough does not open itself — the map and the numbers are the better
 * thing to show someone whose phone is sideways by accident. It can still be
 * asked for, and then it runs, overlap and all.
 */
export function walkthroughFits(floats: boolean, stacked: boolean, mapHeight: number): boolean {
  return floats || stacked || mapHeight - BAR_HEIGHT > mapHeight * MAP_SHARE_UNDER_CARD;
}

/** How far a placed card stands off the edge of the map. */
const CARD_MARGIN = 16;

/**
 * The narrowest a card is asked to read at. A small map has less room beside
 * its fleet than a card's natural width, and a narrower card that stays on the
 * map beats a wider one that has to leave it.
 */
const MIN_CARD_WIDTH = 240;

/**
 * How much map a floating card may hide before it stands under the map
 * instead. Zero would move a card out for clipping the corner of one pin's
 * clearance; much more than this and a card starts sitting on a pin.
 */
const MAX_COVERED = 160;

/**
 * How much of its own box the map keeps when a card stands under it. Below
 * this there is no map left to narrate, and a card that covers part of one is
 * better than a card with nothing to talk about.
 */
const MAP_SHARE_UNDER_CARD = 0.45;

/**
 * What a caption bar under the map costs it, near enough to decide by: a
 * couple of lines and a row of buttons, whatever the card says. Judged from a
 * figure rather than by measuring the bar, because measuring the shape a card
 * only takes when it fits is how it would end up flickering between the two.
 */
const BAR_HEIGHT = 150;

/**
 * Height to place against before the card has been measured. The copy is held
 * to a fixed number of lines, so the real height barely moves from this and a
 * live number ticking inside never shifts the card.
 */
const NOMINAL_HEIGHT = 172;

/** Paging through the intro. Absent on a narration card, which has no past. */
export interface Nav {
  index: number;
  count: number;
  paused: boolean;
  /** performance.now bounds of the card on screen, for the progress ring. */
  startedAt: number;
  endsAt: number;
  pausedAt: number | null;
  back: () => void;
  next: () => void;
  resume: () => void;
}

interface Props {
  card: Card;
  /** Changes only when the words change, so live numbers tick in place. */
  beat: string;
  target: StoryTarget;
  /** Where the map has put things; null before it has drawn. */
  stage: Stage | null;
  nav?: Nav | null;
  /** Browser time of the event a narration card describes. */
  since?: number;
  /**
   * True while the camera is flying to this card's subject. The card glides
   * across with it and holds its words back until it lands: printed straight
   * away they would sit over pins still travelling past.
   */
  travelling?: boolean;
  /**
   * Classes that hold an in-flow card to the page's reading column. Empty
   * beside the panel, where the card already sits in the map's own column.
   */
  column?: string;
  compact?: boolean;
  reducedMotion?: boolean;
  onExit: () => void;
}

/**
 * A walkthrough card: the pattern every product tour uses, floating over the
 * map and pointing at whatever it is talking about.
 *
 * The card belongs to the map. It narrates what is drawn there, so it lives
 * inside the map's own area and never on the page around it — a card beside
 * the numbers is a long way from the arc it is describing. It is placed rather
 * than positioned: the map reports what it has already drawn and the card
 * takes the cheapest spot that covers none of it, and above all none of its
 * own subject. Where no spot on the map is clear it stands in flow directly
 * under the map instead, which still reads as the map talking.
 */
export function StoryCard({
  card,
  beat,
  target,
  stage,
  nav = null,
  since = 0,
  travelling = false,
  column = '',
  compact = false,
  reducedMotion = false,
  onExit,
}: Props) {
  const m = compact ? COMPACT_CARD : FULL_CARD;
  const natural = m.width;
  const [height, setHeight] = useState(NOMINAL_HEIGHT);
  // A card keeps its natural width wherever the map has a clear band to put
  // one in — above the fleet, below it, or beside it. Only where every band is
  // too narrow does it give up width to stay on the map, and then it is
  // rounded to a step so the camera moving underneath cannot resize it a pixel
  // at a time.
  const beside = stage ? Math.max(stage.fleet.x0 - stage.surface.x0, stage.surface.x1 - stage.fleet.x1) - 32 : natural;
  const banded =
    !stage ||
    beside >= natural ||
    stage.fleet.y0 - stage.surface.y0 >= height + 32 ||
    stage.surface.y1 - stage.fleet.y1 >= height + 32;
  const width = banded ? natural : Math.max(MIN_CARD_WIDTH, Math.round(beside / 20) * 20);
  // Measured through a callback ref rather than an effect: no card is rendered
  // at all until the map has reported where things are, so an effect on mount
  // would find nothing to measure and never look again — which left every card
  // placed against the nominal height instead of its own.
  //
  // Only the floating card is measured. A docked one is a different shape, and
  // measuring it would answer the question of whether it fits with the size of
  // the card it turns into when it does not.
  const watching = useRef<ResizeObserver | null>(null);
  const body = useCallback((el: HTMLDivElement | null) => {
    watching.current?.disconnect();
    if (!el) return;
    const measure = () => setHeight(el.getBoundingClientRect().height || NOMINAL_HEIGHT);
    measure();
    watching.current = new ResizeObserver(measure);
    watching.current.observe(el);
  }, []);

  const subject = useSubject(target, stage);
  const placement = useMemo(() => {
    if (!stage || !subject) return null;
    // A card about the whole fleet has no side of anything to stand beside.
    if (target.at === 'fleet') return placeFleetCard(stage.surface, { width, height }, stage.reserved, CARD_MARGIN);
    // Every card stays inside the map: it is narrating what is drawn there,
    // and a card that wanders onto the numbers is a long way from its subject.
    const bounds: Box = stage.surface;
    return placeCard(subject, { width, height }, bounds, stage.reserved, CARD_MARGIN);
  }, [stage, subject, target.at, width, height]);

  // In flow under the map rather than floating inside it, because the map has
  // no room left that the card would not be sitting on. Before the map has
  // reported a stage there is nothing to judge. A stacked page can always find
  // the room by growing; beside the panel the map pays for it, and past the
  // point where that leaves no map the card goes back to covering a corner.
  const mapHeight = stage ? stage.surface.y1 - stage.surface.y0 : 0;
  // Standing under the map shortens the map, which must not then be the reason
  // to climb back on top of it. So the answer is latched until the window
  // changes shape, which is the only thing that can really change it.
  const stood = useRef(false);
  const roomBelow = column !== '' || stood.current || mapHeight - BAR_HEIGHT > mapHeight * MAP_SHARE_UNDER_CARD;
  const docked = !!stage && roomBelow && (!placement || placement.covered > MAX_COVERED);
  useEffect(() => {
    stood.current = docked;
  }, [docked]);
  useEffect(() => {
    const forget = () => {
      stood.current = false;
    };
    window.addEventListener('resize', forget);
    return () => window.removeEventListener('resize', forget);
  }, []);

  const close = (
    <button
      type="button"
      onClick={onExit}
      aria-label="Close the walkthrough"
      className="-mt-1 -mr-1.5 grid size-7 shrink-0 place-items-center rounded-md text-white/35 transition-colors hover:bg-white/5 hover:text-white/80"
    >
      <svg viewBox="0 0 16 16" className="size-3.5" fill="none" stroke="currentColor" strokeWidth="1.6">
        <path d="M4 4l8 8M12 4l-8 8" strokeLinecap="round" />
      </svg>
    </button>
  );

  const words = (
    <>
      <div className="flex items-start justify-between gap-3">
        {nav ? <Sequence nav={nav} /> : <Since at={since} />}
        {!docked && close}
      </div>

      <AnimatePresence mode="wait">
        <motion.div
          // Keyed on the card, not on the words: a live number inside one
          // ticks in place rather than blinking the whole card away.
          key={beat}
          initial={reducedMotion ? { opacity: 0 } : { opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, transition: { duration: 0.16 } }}
          transition={{
            duration: reducedMotion ? 0.2 : 0.42,
            ease: EASE_OUT,
            delay: travelling && !reducedMotion ? 0.55 : 0,
          }}
        >
          <h2 className={`mt-1 leading-tight font-medium tracking-[-0.015em] text-white ${m.title}`}>
            {card.title}
          </h2>
          {/* Room for the sentence whatever it measures, so a number ticking
              over never reflows the card out from under its pointer. Under the
              map the bar is as short as its own words, so it takes none. */}
          <p className={`mt-1.5 leading-snug text-white/60 ${m.body} ${docked ? '' : m.bodyMin}`}>{card.body}</p>
        </motion.div>
      </AnimatePresence>
    </>
  );

  // Under the map the card is a caption bar the width of the map: the words
  // and the controls sit side by side so it costs the map as little height as
  // it can, and wrap when it is too narrow for that.
  const inner = docked ? (
    <div className={`relative flex flex-wrap items-center gap-x-8 gap-y-2 pr-12 ${m.pad}`}>
      <div className="min-w-[16rem] flex-1">{words}</div>
      {nav && <Controls nav={nav} metrics={m} docked reducedMotion={reducedMotion} />}
      {/* Out of the flow: wrapped onto the button row it read as a third
          button rather than as the way out. */}
      <div className="absolute top-2.5 right-2.5">{close}</div>
    </div>
  ) : (
    <div ref={body} className={m.pad}>
      {words}
      {nav && <Controls nav={nav} metrics={m} docked={false} reducedMotion={reducedMotion} />}
    </div>
  );

  // The card is all but opaque, so a backdrop blur only bought a blur of the
  // few per cent of map showing through — and a re-raster of it every frame
  // the map moved underneath.
  const shell =
    'overflow-hidden rounded-2xl border border-white/10 bg-[#0f2340]/95 text-white shadow-[0_24px_80px_rgba(0,0,0,0.55)]';

  if (docked) {
    return (
      <motion.div
        data-story-control
        data-card
        // In flow, immediately under the map and above everything else, so it
        // still reads as the map talking rather than as a page footer.
        className={`pointer-events-auto relative z-30 pt-2 ${column}`}
        // x and scale are named although a card in flow never uses them: the
        // same element carries the floating card, and whatever it was left
        // mid-glide with would otherwise stay on it.
        initial={reducedMotion ? { opacity: 0, x: 0, scale: 1 } : { opacity: 0, y: -12, x: 0, scale: 1 }}
        animate={{ opacity: 1, y: 0, x: 0, scale: 1 }}
        exit={reducedMotion ? { opacity: 0 } : { opacity: 0, y: -12 }}
        transition={{ duration: 0.4, ease: EASE_OUT }}
      >
        <div className={shell}>{inner}</div>
      </motion.div>
    );
  }

  if (!placement) return null;

  const travel = reducedMotion ? 0 : travelling ? 0.75 : 0.35;
  return (
    <motion.div
      data-story-control
      data-card
      className="pointer-events-auto fixed top-0 left-0 z-40"
      style={{ width }}
      // Glides to its new anchor rather than cutting to it, so the card and
      // the camera arrive together and neither snaps past the other.
      initial={reducedMotion ? { opacity: 0, x: placement.x, y: placement.y } : { opacity: 0, scale: 0.97, x: placement.x, y: placement.y }}
      animate={{ opacity: 1, scale: 1, x: placement.x, y: placement.y }}
      exit={{ opacity: 0, transition: { duration: 0.18 } }}
      transition={{
        duration: 0.4,
        ease: EASE_OUT,
        x: { duration: travel, ease: EASE_OUT },
        y: { duration: travel, ease: EASE_OUT },
      }}
    >
      <div className={`relative ${shell}`}>{inner}</div>
      {/* A card about the whole fleet has nothing to point at: the map is the
          subject, and an arrow into the middle of it would only be noise. */}
      {target.at !== 'fleet' && (
        <Pointer key={beat} placement={placement} subject={subject} travelling={travelling} reducedMotion={reducedMotion} />
      )}
    </motion.div>
  );
}

/** The box the card is talking about, in viewport pixels. */
function useSubject(target: StoryTarget, stage: Stage | null): Box | null {
  // A panel is a real element on the page, so it is measured rather than
  // reported; the map republishes its stage on every scroll and resize, which
  // is exactly when that measurement goes stale.
  return useMemo(() => {
    if (!stage) return null;
    if (target.at === 'fleet') return stage.fleet;
    if (target.at === 'location') return stage.subjects[target.location] ?? stage.fleet;
    const el = document.querySelector(`[data-tour="${target.name}"]`);
    if (!el) return stage.fleet;
    const r = el.getBoundingClientRect();
    return { x0: r.left, y0: r.top, x1: r.right, y1: r.bottom };
  }, [target.at, target.at === 'location' ? target.location : target.at === 'panel' ? target.name : '', stage]); // eslint-disable-line react-hooks/exhaustive-deps
}

/**
 * The tail, and a leader to the middle of the subject. Both fade in after the
 * card has landed: drawn during the flight they would trail a pin that has not
 * arrived at the place the card was laid out against.
 */
function Pointer({
  placement,
  subject,
  travelling,
  reducedMotion,
}: {
  placement: CardPlacement;
  subject: Box | null;
  travelling: boolean;
  reducedMotion: boolean;
}) {
  const [tx, ty] = placement.tail;
  const cx = subject ? (subject.x0 + subject.x1) / 2 - placement.x : tx;
  const cy = subject ? (subject.y0 + subject.y1) / 2 - placement.y : ty;
  const length = Math.hypot(cx - tx, cy - ty);
  return (
    <motion.div
      className="pointer-events-none absolute inset-0"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ duration: reducedMotion ? 0 : 0.5, delay: reducedMotion || !travelling ? 0 : 0.7 }}
    >
      <svg className="absolute inset-0 overflow-visible" width="100%" height="100%" aria-hidden>
        {length > 20 && (
          <line
            x1={tx}
            y1={ty}
            x2={cx}
            y2={cy}
            stroke="#E6F59E"
            strokeOpacity={0.3}
            strokeWidth={1}
            strokeDasharray="2 3"
          />
        )}
      </svg>
      <span
        className="absolute size-3 rotate-45 border-t border-l border-white/10 bg-[#0f2340]/95"
        style={{
          left: tx,
          top: ty,
          transform: `translate(-50%, -50%) rotate(${placement.side === 'right' ? 315 : placement.side === 'left' ? 135 : placement.side === 'below' ? 45 : 225}deg)`,
        }}
      />
    </motion.div>
  );
}

/** Where the visitor is in the intro, without pretending it is a wizard. */
function Sequence({ nav }: { nav: Nav }) {
  return (
    <div className="flex items-center gap-1.5 pt-1" aria-label={`Step ${nav.index + 1} of ${nav.count}`}>
      {Array.from({ length: nav.count }, (_, i) => (
        <span
          key={i}
          className={`h-1 rounded-full transition-all duration-500 ${
            i === nav.index ? 'w-5 bg-[#E6F59E]' : i < nav.index ? 'w-1.5 bg-[#E6F59E]/40' : 'w-1.5 bg-white/20'
          }`}
        />
      ))}
    </div>
  );
}

/** A narration card says when, because the answer is always "just now". */
function Since({ at }: { at: number }) {
  const now = useNow(1000);
  const elapsed = Math.max(0, Math.round((now - at) / 1000));
  return (
    <p className="flex items-center gap-2 pt-0.5 text-[12px] font-medium tracking-[0.01em] text-[#E6F59E]/70">
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-[#E6F59E] opacity-60" />
        <span className="relative inline-flex size-1.5 rounded-full bg-[#E6F59E]" />
      </span>
      {elapsed < 5 ? 'Just now' : elapsed < 60 ? `${elapsed}s ago` : `${Math.floor(elapsed / 60)}m ago`}
    </p>
  );
}

function Controls({
  nav,
  metrics,
  docked,
  reducedMotion,
}: {
  nav: Nav;
  metrics: CardMetrics;
  docked: boolean;
  reducedMotion: boolean;
}) {
  return (
    <div className={`flex items-center gap-2 ${docked ? 'shrink-0' : `${metrics.controls} justify-start`}`}>
      <button
        type="button"
        onClick={nav.back}
        className={`rounded-full border border-white/10 font-medium text-white/60 transition-colors hover:bg-white/5 hover:text-white ${metrics.button}`}
      >
        Back
      </button>
      <button
        type="button"
        onClick={nav.next}
        className={`flex items-center justify-center gap-2 rounded-full bg-[#E6F59E] font-medium text-[#0c1d31] transition-colors hover:bg-[#EEF9C0] ${metrics.button} pr-2`}
      >
        Next
        <Ring nav={nav} reducedMotion={reducedMotion} />
      </button>
      {nav.paused && (
        <button
          type="button"
          onClick={nav.resume}
          className={`rounded-full border border-[#E6F59E]/25 bg-[#E6F59E]/[0.07] font-medium text-[#E6F59E] transition-colors hover:bg-[#E6F59E]/15 ${metrics.button}`}
        >
          Resume
        </button>
      )}
    </div>
  );
}

const RING_RADIUS = 7.5;
const RING_LENGTH = 2 * Math.PI * RING_RADIUS;

/**
 * Time passing, on the button that will act when it runs out. It is written
 * straight to the element rather than through state: the card re-rendering
 * thirty times a second to move a ring would reflow the live copy with it.
 */
function Ring({ nav, reducedMotion }: { nav: Nav; reducedMotion: boolean }) {
  const arc = useRef<SVGCircleElement>(null);
  useEffect(() => {
    let raf = 0;
    let peak = 0;
    const draw = () => {
      const span = nav.endsAt - nav.startedAt;
      const at = nav.pausedAt ?? performance.now();
      peak = Math.max(peak, span > 0 ? Math.min(1, (at - nav.startedAt) / span) : 0);
      if (arc.current) arc.current.style.strokeDashoffset = `${RING_LENGTH * (1 - peak)}`;
      // A full ring, or one held at a pause, has nowhere left to go; the next
      // card starts the loop again.
      if (peak < 1 && nav.pausedAt == null) raf = requestAnimationFrame(draw);
    };
    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
    // A new card restarts the ring; pausing freezes it where it stands.
  }, [nav.startedAt, nav.endsAt, nav.pausedAt]);

  return (
    <svg viewBox="0 0 20 20" className={`size-4 -rotate-90 ${nav.paused ? 'opacity-40' : ''}`} aria-hidden>
      <circle cx="10" cy="10" r={RING_RADIUS} fill="none" stroke="#0c1d31" strokeOpacity={0.18} strokeWidth="2" />
      <circle
        ref={arc}
        cx="10"
        cy="10"
        r={RING_RADIUS}
        fill="none"
        stroke="#0c1d31"
        strokeOpacity={0.65}
        strokeWidth="2"
        strokeLinecap="round"
        strokeDasharray={RING_LENGTH}
        strokeDashoffset={reducedMotion ? 0 : RING_LENGTH}
      />
    </svg>
  );
}

/**
 * Offers the walkthrough, or offers it back once the visitor has closed it. A
 * phone has no width for the words beside the line they would interrupt, so
 * there it is usually the play mark alone.
 */
export function PlayStory({
  onClick,
  played = true,
  compact = false,
  iconOnly = false,
}: {
  onClick: () => void;
  /** False where the walkthrough has never run, so this is an offer, not a rerun. */
  played?: boolean;
  compact?: boolean;
  iconOnly?: boolean;
}) {
  const words = played ? 'Replay the tour' : 'Play the tour';
  return (
    <button
      type="button"
      data-story-control
      onClick={onClick}
      aria-label={words}
      title={words}
      className={`flex shrink-0 items-center justify-center gap-2 rounded-full border border-[#E6F59E]/25 bg-[#E6F59E]/[0.07] font-medium whitespace-nowrap text-[#E6F59E] transition-colors hover:bg-[#E6F59E]/15 ${
        iconOnly ? 'size-9' : compact ? 'px-3 py-1.5 text-[11.5px]' : 'px-4 py-2 text-[13px]'
      }`}
    >
      <svg viewBox="0 0 12 12" className="size-3" fill="currentColor" aria-hidden>
        <path d="M2 1.2v9.6L10.5 6z" />
      </svg>
      {!iconOnly && words}
    </button>
  );
}
