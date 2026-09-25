import { useCallback, useEffect, useRef, useState } from "react";

export type Transient<T> = {
  // The value on show, or null once it has timed out or been cleared.
  value: T | null;
  // True for the last `fadeMs` of the value's life, for an exit animation
  // that runs while the value is still rendered. Always false without a
  // fadeMs.
  leaving: boolean;
  // Show a value for the duration. A second show restarts the clock rather
  // than stacking, so a later message is never dismissed early by an
  // earlier one's timer.
  show: (value: T) => void;
  // Drop the value now, and any pending timer with it.
  clear: () => void;
};

// useTransient holds a value that goes away on its own: a stage toast, a
// "Copied" tick on a button, a confirmation line under a form. Six places
// used to keep their own state, timer ref, reset-on-show and unmount cleanup
// for this; one of them forgot the cleanup.
export function useTransient<T>(
  durationMs: number,
  opts: { fadeMs?: number } = {},
): Transient<T> {
  const fadeMs = opts.fadeMs ?? 0;
  const [value, setValue] = useState<T | null>(null);
  const [leaving, setLeaving] = useState(false);
  const timers = useRef<number[]>([]);

  const stop = useCallback(() => {
    for (const id of timers.current) window.clearTimeout(id);
    timers.current = [];
  }, []);

  useEffect(() => stop, [stop]);

  const clear = useCallback(() => {
    stop();
    setValue(null);
    setLeaving(false);
  }, [stop]);

  const show = useCallback(
    (next: T) => {
      stop();
      setValue(next);
      setLeaving(false);
      if (fadeMs > 0) {
        timers.current.push(
          window.setTimeout(() => setLeaving(true), durationMs - fadeMs),
        );
      }
      timers.current.push(
        window.setTimeout(() => {
          setValue(null);
          setLeaving(false);
        }, durationMs),
      );
    },
    [durationMs, fadeMs, stop],
  );

  return { value, leaving, show, clear };
}
