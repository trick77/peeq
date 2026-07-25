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
  | "queue"
  | "channels"
  | "channel"
  | "activity"
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
      { id: "queue", label: "Queue", icon: "download", hot: true },
      { id: "activity", label: "Activity", icon: "listTree" },
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
  queueCount,
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
  /** Badge count for "Queue" — downloads + summaries in flight. Same rule. */
  queueCount?: number;
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
                  : item.id === "queue"
                    ? queueCount
                    : item.count;
              // Inbox and Queue fade out when there is genuinely nothing in
              // them, so the rail reads as a to-do list at a glance. They stay
              // clickable — the page shows its own empty state, and nothing is
              // ever unreachable. The view you are standing on never dims, so
              // an empty Queue you navigated to keeps its active marker legible.
              const idle =
                (item.id === "inbox" || item.id === "queue") &&
                count === 0 &&
                item.id !== active;
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
                  {count !== undefined && count > 0 ? (
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
