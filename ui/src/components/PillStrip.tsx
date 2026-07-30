import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Icon } from "../icons";

// PillStrip keeps a row of filter pills on a single line and scrolls it
// sideways instead of wrapping. A `<`/`>` chevron button appears on each side
// only while there is more to scroll that way, so a row that fits shows no
// chrome at all. The scroll position drives the `l`/`r` classes the CSS uses
// for the chevrons and the edge fades (see .pillstrip in index.css).
//
// `lead` is for a page where this row is the page's first chip row rather than
// a refinement of a status row above it (the Inbox): it drops the negative top
// margin the wrapper otherwise uses to tuck under those status chips.
export function PillStrip({
  children,
  lead,
}: {
  children: ReactNode;
  lead?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ left: false, right: false });

  const update = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    setEdges({
      left: el.scrollLeft > 1,
      right: el.scrollLeft + el.clientWidth < el.scrollWidth - 1,
    });
  }, []);

  // Observe size once. `update` is stable (useCallback []), so this sets up the
  // ResizeObserver and window listener a single time and never tears them down
  // on a parent re-render — the effect deliberately does NOT depend on
  // `children`, whose reference changes every render.
  useEffect(() => {
    update();
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(update);
    ro.observe(el);
    window.addEventListener("resize", update);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", update);
    };
  }, [update]);

  // A changed pill set alters scrollWidth without changing the scroll
  // container's own box size, so the ResizeObserver above may not fire for it —
  // recompute the edges explicitly when the children change, without
  // re-subscribing the observer.
  useEffect(() => {
    update();
  }, [update, children]);

  const nudge = (dir: number) => {
    const el = ref.current;
    if (!el) return;
    el.scrollBy({ left: dir * el.clientWidth * 0.8, behavior: "smooth" });
  };

  return (
    <div
      className={`pillstrip${lead ? " lead" : ""}${edges.left ? " l" : ""}${edges.right ? " r" : ""}`}
    >
      {edges.left && (
        <button
          type="button"
          className="pillstrip-prev"
          aria-label="Scroll filters left"
          onClick={() => nudge(-1)}
        >
          <Icon name="chevronLeft" size="16px" />
        </button>
      )}
      <div className="pillstrip-scroll" ref={ref} onScroll={update}>
        {children}
      </div>
      {edges.right && (
        <button
          type="button"
          className="pillstrip-next"
          aria-label="Scroll filters right"
          onClick={() => nudge(1)}
        >
          <Icon name="chevronRight" size="16px" />
        </button>
      )}
    </div>
  );
}
