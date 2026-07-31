import { useEffect, useRef, useState } from "react";
import { Icon } from "../icons";
import { MORE_ITEMS, TAB_ITEMS, type NavItem, type ViewId } from "./nav";

// TabBar — the phone's navigation, in place of the rail (which is not rendered
// below the breakpoint at all). Modelled on ../music's tab bar rather than
// loom's off-canvas drawer, and the reason is what peeq is: a player is hopped
// around one-handed, so a destination should cost one thumb tap, not open-pick-
// dismiss. loom's drawer earns its two taps by holding a scrolling list of
// threads; peeq's nav is nine fixed places, four of which are where you
// actually go.
//
// The other five live behind "More". That sheet is the same scrim/Escape
// pattern as ConfirmDialog, deliberately: a phone has one modal idiom and this
// is it.
export function TabBar({
  active,
  onNavigate,
  pendingCount,
  upNextCount,
  upNextLive,
}: {
  active: ViewId;
  onNavigate: (view: ViewId) => void;
  pendingCount?: number;
  upNextCount?: number;
  upNextLive?: boolean;
}) {
  const [moreOpen, setMoreOpen] = useState(false);
  const moreRef = useRef<HTMLButtonElement>(null);
  const sheetRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!moreOpen) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") closeMore();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [moreOpen]);

  // Focus goes into the sheet when it opens and back to the tab that opened it
  // when it closes — ConfirmDialog's contract, which this claims to share. A
  // modal that leaves focus on a control outside its own scrim strands a screen
  // reader beyond the thing it just opened.
  useEffect(() => {
    if (!moreOpen) return;
    sheetRef.current?.querySelector<HTMLElement>("button")?.focus();
  }, [moreOpen]);

  function closeMore() {
    setMoreOpen(false);
    moreRef.current?.focus();
  }

  // Same rule the rail uses: a count is shown when it is loaded and non-zero,
  // and Up next additionally needs something actually running — a number that
  // never falls reads as progress when nothing is happening.
  function countFor(item: NavItem): number | undefined {
    const count =
      item.id === "inbox"
        ? pendingCount
        : item.id === "upnext"
          ? upNextCount
          : item.count;
    if (count === undefined || count <= 0) return undefined;
    if (item.id === "upnext" && upNextLive !== true) return undefined;
    return count;
  }

  function go(id: ViewId) {
    if (moreOpen) closeMore();
    onNavigate(id);
  }

  // A destination inside "More" still lights the More tab, otherwise the bar
  // claims you are nowhere while you are on Settings.
  const inMore = MORE_ITEMS.some((i) => i.id === active);

  return (
    <>
      {moreOpen ? (
        <div
          className="tabbar-sheet-scrim"
          onMouseDown={(e) => {
            if (e.target === e.currentTarget) closeMore();
          }}
        >
          {/* aria-modal is not decoration: the "/" shortcut in SearchField
              stands down for `[role="dialog"][aria-modal="true"]`, so without
              it a keystroke over this sheet pulls focus to a search field
              behind the scrim and leaves the open sheet with nothing focused. */}
          <div
            ref={sheetRef}
            className="tabbar-sheet"
            role="dialog"
            aria-modal="true"
            aria-label="More"
          >
            {MORE_ITEMS.map((item) => {
              const count = countFor(item);
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`tabbar-sheet-item${item.id === active ? " active" : ""}`}
                  onClick={() => go(item.id)}
                  aria-current={item.id === active ? "page" : undefined}
                >
                  <Icon
                    name={item.icon}
                    size="20px"
                    style={{ width: 20, height: 20 }}
                  />
                  {item.label}
                  {count !== undefined ? (
                    <span className={`rail-nav-count${item.hot ? " hot" : ""}`}>
                      {count}
                    </span>
                  ) : null}
                </button>
              );
            })}
          </div>
        </div>
      ) : null}

      <nav className="tabbar" aria-label="Main">
        {TAB_ITEMS.map((item) => {
          const count = countFor(item);
          return (
            <button
              key={item.id}
              type="button"
              className={`tabbar-item${item.id === active ? " active" : ""}`}
              onClick={() => go(item.id)}
              aria-current={item.id === active ? "page" : undefined}
              // The visible label is 10px and truncates on a narrow phone; the
              // count is a dot with no number in it. Both are said in full
              // here, so a screen reader is never worse off than the eye.
              aria-label={
                count !== undefined ? `${item.label}, ${count}` : item.label
              }
            >
              <span className="tabbar-ic">
                <Icon
                  name={item.icon}
                  size="22px"
                  style={{ width: 22, height: 22 }}
                />
                {count !== undefined ? (
                  <span
                    className={`rail-nav-dot${item.hot ? " hot" : ""}`}
                    aria-hidden="true"
                  />
                ) : null}
              </span>
              <span className="tabbar-label">{item.label}</span>
            </button>
          );
        })}
        <button
          ref={moreRef}
          type="button"
          className={`tabbar-item${inMore ? " active" : ""}`}
          onClick={() => setMoreOpen((v) => !v)}
          aria-label="More"
          aria-expanded={moreOpen}
          aria-haspopup="dialog"
        >
          <span className="tabbar-ic">
            <Icon name="more" size="22px" style={{ width: 22, height: 22 }} />
          </span>
          <span className="tabbar-label">More</span>
        </button>
      </nav>
    </>
  );
}
