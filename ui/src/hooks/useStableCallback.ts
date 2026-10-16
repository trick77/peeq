import { useCallback, useInsertionEffect, useRef } from "react";

// useStableCallback returns a function whose identity never changes and which
// always calls the latest fn. For handing a callback to a memoised child when
// the caller cannot promise its own is stable: the child's memo then holds,
// and the call still reaches the current closure.
export function useStableCallback<A extends unknown[], R>(
  fn: (...args: A) => R,
): (...args: A) => R {
  const ref = useRef(fn);
  // An insertion effect runs before every layout effect of the commit, the
  // children's included: a child calling back from its own layout effect
  // already reaches this render's fn.
  useInsertionEffect(() => {
    ref.current = fn;
  });
  return useCallback((...args: A) => ref.current(...args), []);
}
