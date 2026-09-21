import { useEffect, useRef } from 'react';
import { usePrefersReducedMotion } from '../viewport';

interface Props {
  value: number;
  className?: string;
}

/**
 * Counts continuously between polls. Polls arrive every two seconds, so the
 * counter extrapolates at the observed rate instead of jumping, and never
 * runs further ahead than one missed poll.
 */
export function LiveCounter({ value, className }: Props) {
  const reducedMotion = usePrefersReducedMotion();
  const ref = useRef<HTMLSpanElement>(null);
  const sample = useRef({ value, at: performance.now(), rate: 0, shown: value });

  useEffect(() => {
    const s = sample.current;
    const now = performance.now();
    const dt = (now - s.at) / 1000;
    if (dt > 0.2 && value >= s.value) {
      const observed = (value - s.value) / dt;
      s.rate = s.rate === 0 ? observed : s.rate * 0.5 + observed * 0.5;
    }
    if (value < s.value) {
      // The fleet shrank; follow the new total without animating backwards.
      s.shown = value;
      s.rate = 0;
    }
    s.value = value;
    s.at = now;
  }, [value]);

  useEffect(() => {
    const fmt = new Intl.NumberFormat('en-US');
    // With reduced motion the counter simply shows each polled total.
    if (reducedMotion) {
      if (ref.current) ref.current.textContent = fmt.format(value);
      return;
    }
    let raf = 0;
    let printed = '';
    const tick = () => {
      const s = sample.current;
      const elapsed = Math.min((performance.now() - s.at) / 1000, 3);
      const target = s.value + s.rate * elapsed;
      // Ease towards the target so corrections are never visible as jumps.
      s.shown = Math.max(s.shown, s.shown + (target - s.shown) * 0.12);
      const next = fmt.format(Math.floor(s.shown));
      // The total gains a few hundred a second, so most frames have nothing
      // new to print; writing anyway would relayout the panel around it.
      if (next !== printed && ref.current) {
        ref.current.textContent = next;
        printed = next;
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [reducedMotion, value]);

  return (
    <span ref={ref} className={className}>
      {new Intl.NumberFormat('en-US').format(value)}
    </span>
  );
}
