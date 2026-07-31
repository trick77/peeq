import { useEffect, useRef } from "react";
import type { YtdlpVersion } from "../api/ytdlp";
import { Icon } from "../icons";
import { CookieStatus } from "./CookieStatus";
import { YtdlpStatus } from "./YtdlpStatus";
import { SECTIONS, type ViewId } from "./nav";

// ViewId and the nav list moved to ./nav, which the mobile tab bar reads too.
// Re-exported here because Rail was where the rest of the app imported it from.
export type { ViewId } from "./nav";

export function Rail({
  active,
  onNavigate,
  collapsed = false,
  onToggleCollapsed,
  pendingCount,
  upNextCount,
  upNextLive,
  cookieStatus,
  cookieUpdatedAtLabel,
  ytdlp,
}: {
  active: ViewId;
  onNavigate: (view: ViewId) => void;
  /**
   * Icon-only rail. App owns the flag because the same state also swaps the
   * shell's grid track, and it is already derived there: on a phone the rail is
   * not rendered at all, so this is never true for want of room.
   */
  collapsed?: boolean;
  onToggleCollapsed?: () => void;
  /**
   * Badge count for "Inbox" — new uploads awaiting a keep/ignore decision.
   * Deliberately not defaulted to 0: `undefined` means "not loaded yet", which
   * is not the same claim as "there are none". A default would have the pill
   * assert an empty inbox on every cold paint until the first fetch lands.
   */
  pendingCount?: number;
  /**
   * Work in "Up next" — running plus waiting, across both lanes. Same
   * undefined-is-not-zero rule as pendingCount. Housekeeping (scans,
   * metadata refreshes, retention) is never counted: it is scheduled work peeq
   * does on its own, not a backlog anyone is waiting on.
   */
  upNextCount?: number;
  /**
   * Whether a download or a summary is actually RUNNING. The pill needs both:
   * a count says how much there is, this says it is moving. Waiting-but-frozen
   * work — everything paused, YouTube blocked — shows no pill, because a number
   * that never falls reads as progress when nothing is happening. The pause
   * banner above the page is the louder signal about why.
   */
  upNextLive?: boolean;
  cookieStatus?: string;
  cookieUpdatedAtLabel?: string;
  /**
   * The yt-dlp version report. The indicator below renders only while this
   * says an update is waiting — undefined (not yet loaded) and an up-to-date
   * binary both show nothing.
   */
  ytdlp?: YtdlpVersion;
}) {
  // Collapsing unmounts every label, the section headings and the foot dock. If
  // focus was inside any of that it would be dropped on the floor and the next
  // Tab would start again from the top of the document, so it moves to the
  // control that caused it — which is also the control that undoes it.
  const toggleRef = useRef<HTMLButtonElement>(null);
  const wasCollapsed = useRef(collapsed);
  useEffect(() => {
    if (collapsed !== wasCollapsed.current) {
      wasCollapsed.current = collapsed;
      if (collapsed && toggleRef.current?.contains(document.activeElement))
        return;
      if (collapsed) toggleRef.current?.focus();
    }
  }, [collapsed]);

  return (
    <aside className={`rail${collapsed ? " collapsed" : ""}`}>
      <div className="rail-brand">
        {collapsed ? null : <b>Peeq</b>}
        {onToggleCollapsed ? (
          <button
            ref={toggleRef}
            type="button"
            className="rail-collapse"
            onClick={onToggleCollapsed}
            aria-label={collapsed ? "Show sidebar" : "Hide sidebar"}
            aria-expanded={!collapsed}
          >
            <Icon name="panelLeft" size="18px" />
          </button>
        ) : null}
      </div>

      <nav className="rail-nav">
        {SECTIONS.map((section) => (
          // display:contents keeps each section's items as direct flex
          // children of .rail-nav, so the 2px gap applies within a section
          // (not just between sections) — matches the mockup.
          <div key={section.label} style={{ display: "contents" }}>
            {collapsed ? null : (
              <div className="rail-nav-label">{section.label}</div>
            )}
            {section.items.map((item) => {
              const count =
                item.id === "inbox"
                  ? pendingCount
                  : item.id === "upnext"
                    ? upNextCount
                    : item.count;
              // Nothing dims. Inbox and Up next used to fade to 42% when empty
              // so the rail read as a to-do list; they no longer do, because a
              // nav item whose strength changes under you is harder to aim at
              // than one that always looks the same, and an empty page is not a
              // lesser destination. Emptiness is said by the absent count pill
              // and by the page's own empty state, both of which are honest
              // without touching the item's weight.
              //
              // Up next needs something running before it shows a
              // number — see upNextLive. Every other counted item shows its
              // count whenever it has one.
              const showCount =
                count !== undefined &&
                count > 0 &&
                (item.id !== "upnext" || upNextLive === true);
              // Collapsed, the label is unmounted rather than hidden — nothing
              // about it animates, only the shell's grid track does — so the
              // button would lose its accessible name. aria-label carries it,
              // and carries the count with it: a dot can say "there is
              // something" but not how much, and a screen reader gets neither
              // from a bare glyph.
              const ariaLabel = collapsed
                ? showCount
                  ? `${item.label}, ${count}`
                  : item.label
                : undefined;
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`rail-nav-item${item.id === active ? " active" : ""}`}
                  onClick={() => onNavigate(item.id)}
                  aria-current={item.id === active ? "page" : undefined}
                  aria-label={ariaLabel}
                >
                  <span className="rail-nav-ic">
                    <Icon
                      name={item.icon}
                      size={collapsed ? "22px" : "18px"}
                      style={
                        collapsed
                          ? { width: 22, height: 22 }
                          : { width: 18, height: 18 }
                      }
                    />
                    {/* Collapsed there is no room beside a centred icon for the
                        count pill, and dropping the signal outright would cost
                        the rail the one thing it says about the Inbox. The dot
                        keeps "there is something in there" at a size that
                        fits; the number is in the aria-label above. */}
                    {collapsed && showCount ? (
                      <span
                        className={`rail-nav-dot${item.hot ? " hot" : ""}`}
                        aria-hidden="true"
                      />
                    ) : null}
                  </span>
                  {collapsed ? null : item.label}
                  {!collapsed && showCount ? (
                    <span className={`rail-nav-count${item.hot ? " hot" : ""}`}>
                      {count}
                    </span>
                  ) : null}
                </button>
              );
            })}
          </div>
        ))}
      </nav>

      {/* The dock says two sentences — a cookie's age, a yt-dlp release — and
          neither survives 64px. It goes when the rail collapses, the way it
          already goes on a phone. */}
      <div className="rail-foot">
        {collapsed ? null : cookieStatus !== undefined ? (
          <CookieStatus
            status={cookieStatus}
            updatedAtLabel={cookieUpdatedAtLabel}
          />
        ) : null}
        {!collapsed && ytdlp?.update_available && ytdlp.latest ? (
          <YtdlpStatus version={ytdlp.version} latest={ytdlp.latest} />
        ) : null}
      </div>
    </aside>
  );
}
