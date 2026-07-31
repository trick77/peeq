import { useEffect, useState } from "react";

/**
 * useMediaQuery — subscribes to a media query and re-renders on change.
 * Ported from ../loom/ui/src/chat/useMediaQuery.ts.
 *
 * The `typeof window` guards are not decoration: App.test.tsx renders the app
 * through renderToStaticMarkup, where there is no window and no matchMedia.
 * Unguarded, the hook would throw there rather than in a browser.
 *
 * Anything that also has a CSS side must use the SAME breakpoint written the
 * other way round — this hook asks "(max-width: 767px)", index.css asks
 * "(min-width: 768px)". Change one and the layout and the markup disagree about
 * what a phone is.
 */
export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() =>
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia(query).matches
      : false,
  );
  useEffect(() => {
    if (
      typeof window === "undefined" ||
      typeof window.matchMedia !== "function"
    )
      return;
    const mql = window.matchMedia(query);
    const onChange = (event: MediaQueryListEvent) => setMatches(event.matches);
    setMatches(mql.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, [query]);
  return matches;
}

/** The one place the phone breakpoint is written for JS. */
export const MOBILE_QUERY = "(max-width: 767px)";
