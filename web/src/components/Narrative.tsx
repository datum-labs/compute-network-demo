import { AnimatePresence, motion } from 'motion/react';
import { useEffect, useState } from 'react';

const DISCOVER_VIA_API =
  'Each Instance finds its peers through the Datum Cloud API, then talks to them privately across regions.';
const DISCOVER_STATIC =
  'Each Instance talks to its peers across regions, over the one private network they share.';

const LINES = [
  DISCOVER_VIA_API,
  'Every arc is real traffic between Instances, timed as it happens. Zoom in on a location to see the Instances there.',
  'Add an Instance and it joins the network by itself. Take one away and the rest carry on.',
];

// A phone fits about two short lines, so it gets its own wording rather than
// a sentence cut in half.
const COMPACT_LINES = [
  'Every Instance talks to the others over a private network.',
  'Each arc is real traffic. Pinch to zoom into a location.',
  'Add an Instance and it joins the network by itself.',
];
const COMPACT_DISCOVER_STATIC = 'Instances talk to each other privately, across regions.';

const ROTATE_MS = 9000;

export function Narrative({
  staticDiscovery = false,
  compact = false,
  short = false,
  paused = false,
}: {
  staticDiscovery?: boolean;
  /** Smaller type. */
  compact?: boolean;
  /** Phone-length wording, which fits two lines. */
  short?: boolean;
  paused?: boolean;
}) {
  const [index, setIndex] = useState(0);
  useEffect(() => {
    if (paused) return;
    const id = window.setInterval(() => setIndex((i) => (i + 1) % LINES.length), ROTATE_MS);
    return () => window.clearInterval(id);
  }, [paused]);

  return (
    <div className={`flex ${compact ? 'items-start gap-3' : 'items-center gap-6'}`}>
      <div className={`flex shrink-0 gap-1.5 ${compact ? '-mt-1' : ''}`}>
        {LINES.map((line, i) => (
          <button
            key={line}
            type="button"
            aria-label={`Show caption ${i + 1}`}
            onClick={() => setIndex(i)}
            // A tall transparent hit area keeps the dots tappable.
            className={`group flex w-4 items-center justify-center ${compact ? 'h-9' : 'h-11'}`}
          >
            <span
              className={`h-1 rounded-full transition-all duration-500 ${
                i === index ? 'w-6 bg-[#E6F59E]' : 'w-1.5 bg-white/20 group-hover:bg-white/40'
              }`}
            />
          </button>
        ))}
      </div>
      <div className={`relative flex-1 ${compact ? 'min-h-[38px]' : 'min-h-[56px]'}`}>
        <AnimatePresence mode="wait">
          <motion.p
            key={index}
            className={`absolute inset-0 flex items-start leading-snug font-normal tracking-[-0.01em] ${
              compact
                ? `${short ? 'line-clamp-2 text-[13.5px]' : 'text-[15px]'} text-white/75`
                : 'items-center text-[20px] text-white/80'
            }`}
            initial={{ opacity: 0, y: 10, filter: 'blur(4px)' }}
            animate={{ opacity: 1, y: 0, filter: 'blur(0px)' }}
            exit={{ opacity: 0, y: -8, filter: 'blur(4px)' }}
            transition={{ duration: 0.6, ease: [0.22, 1, 0.36, 1] }}
          >
            {short
              ? index === 0 && staticDiscovery
                ? COMPACT_DISCOVER_STATIC
                : COMPACT_LINES[index]
              : index === 0 && staticDiscovery
                ? DISCOVER_STATIC
                : LINES[index]}
          </motion.p>
        </AnimatePresence>
      </div>
    </div>
  );
}
