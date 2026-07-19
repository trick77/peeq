import { Icon, type IconName } from "../icons";
import { DownloadDock } from "./DownloadDock";
import { CookieStatus } from "./CookieStatus";
import type { Job } from "../api/types";

// ViewId enumerates the six destinations the rail routes to. App.tsx owns
// the actual view-state (manual, no router lib — see App.tsx); Rail is
// purely presentational plus the onNavigate callback.
export type ViewId = "library" | "player" | "add" | "pending" | "channels" | "settings";

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
    ],
  },
  {
    label: "Collect",
    items: [
      { id: "add", label: "Add a video", icon: "plus" },
      { id: "pending", label: "New & pending", icon: "clock", hot: true },
      { id: "channels", label: "Channels", icon: "tv" },
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
  pendingCount = 0,
  jobs = [],
  progressByJobId,
  cookieStatus,
  cookieUpdatedAtLabel,
}: {
  active: ViewId;
  onNavigate: (view: ViewId) => void;
  /** Badge count for "New & pending". */
  pendingCount?: number;
  jobs?: Job[];
  /** Live per-job percent/speed/eta from the download SSE feed. */
  progressByJobId?: Record<number, { percent: number; speed: string; eta: string }>;
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
            v<span>a</span>rk
          </b>
          <small>video archive</small>
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
              const count = item.id === "pending" ? pendingCount : item.count;
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`rail-nav-item${item.id === active ? " active" : ""}`}
                  onClick={() => onNavigate(item.id)}
                  aria-current={item.id === active ? "page" : undefined}
                >
                  <Icon name={item.icon} size="18px" style={{ width: 18, height: 18 }} />
                  {item.label}
                  {count !== undefined && count > 0 ? (
                    <span className={`rail-nav-count${item.hot ? " hot" : ""}`}>{count}</span>
                  ) : null}
                </button>
              );
            })}
          </div>
        ))}
      </nav>

      <div className="rail-foot">
        <DownloadDock jobs={jobs} progressByJobId={progressByJobId} />
        {cookieStatus !== undefined ? (
          <CookieStatus status={cookieStatus} updatedAtLabel={cookieUpdatedAtLabel} />
        ) : null}
      </div>
    </aside>
  );
}
