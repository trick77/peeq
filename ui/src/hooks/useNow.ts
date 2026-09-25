import { useEffect, useState } from "react";

// useNow is "the current time", re-read every `everyMs`. A page that captures
// Date.now() in render gets a different value on every render, which makes
// any memo keyed on it useless — and a page that never re-reads it shows
// "in 5 minutes" long after the five minutes are up.
export function useNow(everyMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), everyMs);
    return () => window.clearInterval(id);
  }, [everyMs]);
  return now;
}
