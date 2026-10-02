import { useEffect, useState } from "react";

// useDebouncedValue returns value once it has stopped changing for delayMs, so
// typing "abyss" into a search box fires one request, not five. initial is
// what it reads until the first delay has passed; it defaults to value.
export function useDebouncedValue<T>(
  value: T,
  delayMs: number,
  initial: T = value,
): T {
  const [debounced, setDebounced] = useState(initial);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(id);
  }, [value, delayMs]);
  return debounced;
}
