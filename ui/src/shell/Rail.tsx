import { Icon, type IconName } from "../icons";
import { CookieStatus } from "./CookieStatus";

// ViewId enumerates the destinations App routes to. App.tsx owns the actual
// view-state (manual, no router lib — see App.tsx); Rail is purely
// presentational plus the onNavigate callback.
//
// "channel" is a detail destination reached by clicking a channel name, not
// a rail entry — deliberately absent from SECTIONS below, like "player" is
// reached from a video card. Rail's `active` simply matches nothing then.
export type ViewId =
  | "library"
  | "player"
  | "search"
  | "add"
  | "inbox"
  | "upnext"
  | "channels"
  | "channel"
  | "history"
  | "settings"
  // share is the public /s/<token> page — reached only by a share link, never
  // a rail item, and rendered chromeless (no rail/top bar) above the app shell.
  | "share";

type NavItem = {
  id: ViewId;
  label: string;
  icon: IconName;
  count?: number;
  hot?: boolean;
};

// Sectioned per the mockup: Watch / Collect / Setup.
const SECTIONS: { label: string; items: NavItem[] }[] = [
  {
    label: "Watch",
    items: [
      { id: "library", label: "Library", icon: "library" },
      // Channels sits directly under Library: it is the other way you browse
      // what you already have, not part of collecting more.
      { id: "channels", label: "Channels", icon: "tv" },
      { id: "player", label: "Now playing", icon: "circlePlay" },
      { id: "search", label: "Search", icon: "search" },
    ],
  },
  {
    label: "Collect",
    items: [
      { id: "add", label: "Add", icon: "plus" },
      { id: "inbox", label: "Inbox", icon: "inbox", hot: true },
      // Up next and History are the same question asked in two directions —
      // what peeq is about to do, and what it already did. They sit adjacent so
      // the pair is obvious; the old Queue/Activity split put the two halves of
      // "what is peeq doing" on pages in different visual languages.
      { id: "upnext", label: "Up next", icon: "clockArrowUp", hot: true },
      { id: "history", label: "History", icon: "history" },
    ],
  },
  {
    label: "Setup",
    items: [{ id: "settings", label: "Settings", icon: "settings" }],
  },
];

export function Rail({
  active,
  onNavigate,
  pendingCount,
  upNextCount,
  upNextLive,
  cookieStatus,
  cookieUpdatedAtLabel,
}: {
  active: ViewId;
  onNavigate: (view: ViewId) => void;
  /**
   * Badge count for "Inbox" — new uploads awaiting a keep/ignore decision.
   * Deliberately not defaulted to 0: `undefined` means "not loaded yet", and
   * only a real 0 dims the item (see `idle` below). A default would grey the
   * rail on every cold paint until the first fetch lands.
   */
  pendingCount?: number;
  /**
   * Work in "Up next" — running plus waiting, across both lanes. Same
   * undefined-means-unloaded rule as pendingCount. Housekeeping (scans,
   * metadata refreshes, retention) is never counted: it is scheduled work peeq
   * does on its own, not a backlog anyone is waiting on.
   */
  upNextCount?: number;
  /**
   * Whether a download or a summary is actually RUNNING. The pill needs both:
   * a count says how much there is, this says it is moving. Waiting-but-frozen
   * work — everything paused, YouTube blocked — shows no pill, because a number
   * that never falls reads as progress when nothing is happening. The item
   * still doesn't dim (the count is non-zero), and the pause banner above the
   * page is the louder signal about why.
   */
  upNextLive?: boolean;
  cookieStatus?: string;
  cookieUpdatedAtLabel?: string;
}) {
  return (
    <aside className="rail">
      <div className="rail-brand">
        <div className="rail-logo">
          <Icon name="playFilled" size="0.9rem" />
        </div>
        <div>
          <b>
            p<span>ee</span>q
          </b>
        </div>
      </div>

      <nav className="rail-nav">
        {SECTIONS.map((section) => (
          // display:contents keeps each section's items as direct flex
          // children of .rail-nav, so the 2px gap applies within a section
          // (not just between sections) — matches the mockup.
          <div key={section.label} style={{ display: "contents" }}>
            <div className="rail-nav-label">{section.label}</div>
            {section.items.map((item) => {
              const count =
                item.id === "inbox"
                  ? pendingCount
                  : item.id === "upnext"
                    ? upNextCount
                    : item.count;
              // Inbox and Up next fade out when there is genuinely nothing in
              // them, so the rail reads as a to-do list at a glance. They stay
              // clickable — the page shows its own empty state, and nothing is
              // ever unreachable. The view you are standing on never dims, so
              // an empty Up next you navigated to keeps its active marker
              // legible. History never dims: a log is never "empty to do".
              const idle =
                (item.id === "inbox" || item.id === "upnext") &&
                count === 0 &&
                item.id !== active;
              // Up next additionally needs something running before it shows a
              // number — see upNextLive. Every other counted item shows its
              // count whenever it has one.
              const showCount =
                count !== undefined &&
                count > 0 &&
                (item.id !== "upnext" || upNextLive === true);
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`rail-nav-item${item.id === active ? " active" : ""}${idle ? " idle" : ""}`}
                  onClick={() => onNavigate(item.id)}
                  aria-current={item.id === active ? "page" : undefined}
                >
                  <Icon
                    name={item.icon}
                    size="18px"
                    style={{ width: 18, height: 18 }}
                  />
                  {item.label}
                  {showCount ? (
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

      <div className="rail-foot">
        {cookieStatus !== undefined ? (
          <CookieStatus
            status={cookieStatus}
            updatedAtLabel={cookieUpdatedAtLabel}
          />
        ) : null}
      </div>
    </aside>
  );
}
