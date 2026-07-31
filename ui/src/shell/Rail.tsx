import type { YtdlpVersion } from "../api/ytdlp";
import { Icon, type IconName } from "../icons";
import { CookieStatus } from "./CookieStatus";
import { YtdlpStatus } from "./YtdlpStatus";

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

// PeeqMark is the lockup's mark: the same glyph the browser tab wears, so the
// app and its favicon are one brand rather than two. The geometry is copied
// from ui/public/icon.svg — lucide "square-play" (ISC), wedge filled rather
// than stroked, frame at stroke 2.5 — and NOT imported through the Icon set,
// which draws one flat currentColor and cannot carry the gradient.
//
// One departure from the file it came from: the colors are the accent tokens,
// not its literal #e08a68/#c25f34. The bottom stop is the same value either
// way, and the app's CSS is token-based — the tile this replaced used exactly
// this strong→fill pair.
//
// Nothing else differs any more. This used to also paint no ground where
// icon.svg painted two — a #1f1f1e canvas and a fill inside the frame, both
// there to stop a browser's own chrome showing through a favicon. The favicon
// dropped them, so the mark is now the same outline in both places: here it
// lets the rail's panel→bg gradient through, on a tab it takes the tab's
// colour, and that is one behaviour rather than two.
//
// The viewBox is the frame's TRUE bbox (x/y 3→21 in glyph units, grown by half
// the 2.5 stroke) rather than the nominal 24x24 the paths only partly fill, so
// the mark spans its 30px box edge to edge instead of shrinking to a dot in the
// middle of it — the same sizing rule icon.svg's own comment sets out.
function PeeqMark() {
  return (
    <svg className="rail-logo" viewBox="1.75 1.75 20.5 20.5" aria-hidden="true">
      <linearGradient id="rail-mark-grad" x1="0" y1="0" x2="0.72" y2="1">
        <stop className="s0" offset="0" />
        <stop className="s1" offset="1" />
      </linearGradient>
      <rect
        x="3"
        y="3"
        width="18"
        height="18"
        rx="2"
        fill="none"
        stroke="url(#rail-mark-grad)"
        strokeWidth="2.5"
        strokeLinejoin="round"
      />
      <path
        d="M9 9.003a1 1 0 0 1 1.517-.859l4.997 2.997a1 1 0 0 1 0 1.718l-4.997 2.997A1 1 0 0 1 9 14.996z"
        fill="url(#rail-mark-grad)"
      />
    </svg>
  );
}

export function Rail({
  active,
  onNavigate,
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
  return (
    <aside className="rail">
      <div className="rail-brand">
        <PeeqMark />
        <b>
          P<span>ee</span>q
        </b>
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
        {ytdlp?.update_available && ytdlp.latest ? (
          <YtdlpStatus version={ytdlp.version} latest={ytdlp.latest} />
        ) : null}
      </div>
    </aside>
  );
}
