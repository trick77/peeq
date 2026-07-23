import { Icon, type IconName } from "../icons";
import { StatusPanel } from "./StatusPanel";
import { CookieStatus } from "./CookieStatus";
import type { Job } from "../api/types";
import type { DownloadsStatus } from "../api/downloads";

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
  | "decide"
  | "queue"
  | "channels"
  | "channel"
  | "activity"
  | "settings";

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
      { id: "player", label: "Now playing", icon: "circlePlay" },
      { id: "search", label: "Search", icon: "search" },
    ],
  },
  {
    label: "Collect",
    items: [
      { id: "add", label: "Add", icon: "plus" },
      { id: "decide", label: "Decide", icon: "clock", hot: true },
      { id: "queue", label: "Queue", icon: "download" },
      { id: "channels", label: "Channels", icon: "tv" },
    ],
  },
  {
    label: "Setup",
    items: [
      { id: "activity", label: "Activity", icon: "listTree" },
      { id: "settings", label: "Settings", icon: "settings" },
    ],
  },
];

export function Rail({
  active,
  onNavigate,
  pendingCount = 0,
  queueCount = 0,
  summarizingCount = 0,
  jobs = [],
  progressByJobId,
  cookieStatus,
  cookieUpdatedAtLabel,
  downloadStatus,
}: {
  active: ViewId;
  onNavigate: (view: ViewId) => void;
  /** Badge count for "Decide" — uploads awaiting a keep/ignore decision. */
  pendingCount?: number;
  /** Badge count for "Queue" — downloads + summaries in flight. */
  queueCount?: number;
  /** How many summaries are in flight; feeds the status panel's own row. */
  summarizingCount?: number;
  jobs?: Job[];
  /** Live per-job percent/speed/eta from the download SSE feed. */
  progressByJobId?: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
  cookieStatus?: string;
  cookieUpdatedAtLabel?: string;
  /** Why the queue may be stalled; shown as the panel's state word. */
  downloadStatus?: DownloadsStatus;
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
                item.id === "decide"
                  ? pendingCount
                  : item.id === "queue"
                    ? queueCount
                    : item.count;
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`rail-nav-item${item.id === active ? " active" : ""}`}
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
        <StatusPanel
          jobs={jobs}
          progressByJobId={progressByJobId}
          pendingCount={pendingCount}
          summarizingCount={summarizingCount}
          status={downloadStatus}
          onOpenPending={() => onNavigate("decide")}
          onOpenQueue={() => onNavigate("queue")}
        />
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
