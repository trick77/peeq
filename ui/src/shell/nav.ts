import type { IconName } from "../icons";

// ViewId enumerates the destinations App routes to. App.tsx owns the actual
// view-state (manual, no router lib — see App.tsx); the rail and the mobile
// tab bar are purely presentational plus the onNavigate callback.
//
// "channel" is a detail destination reached by clicking a channel name, not
// a nav entry — deliberately absent from SECTIONS below, like "player" is
// reached from a video card. `active` simply matches nothing then.
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
  // a nav item, and rendered chromeless (no rail/top bar) above the app shell.
  | "share";

export type NavItem = {
  id: ViewId;
  label: string;
  icon: IconName;
  count?: number;
  hot?: boolean;
};

// Sectioned per the mockup: Watch / Collect / Setup.
//
// This list lives here rather than in Rail.tsx because two components render
// it now: the desktop rail and the mobile tab bar. One list means a
// destination added here appears in both, which a second copy would not
// guarantee.
export const SECTIONS: { label: string; items: NavItem[] }[] = [
  {
    label: "Watch",
    items: [
      // First, above Library. Now that playback survives leaving the player
      // page, this is the way back to something already in progress rather
      // than a place you go to start one — and what you are half-way through
      // is a better default destination than the shelf it came off.
      { id: "player", label: "Now playing", icon: "circlePlay" },
      { id: "library", label: "Library", icon: "library" },
      // Channels sits directly under Library: it is the other way you browse
      // what you already have, not part of collecting more.
      { id: "channels", label: "Channels", icon: "tv" },
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

export const ALL_NAV_ITEMS: NavItem[] = SECTIONS.flatMap((s) => s.items);

// The four destinations that get a tab of their own on a phone. A tab bar holds
// about five targets before they stop being thumb-sized, and the rail holds
// nine, so the split is deliberate rather than arithmetic: these four are where
// you go to watch or to decide, which is what a phone is used for. The other
// five sit one tap deeper behind "More" — Search and Add because both start
// with typing, Channels/History/Settings because they are read rarely.
export const TAB_IDS: ViewId[] = ["library", "player", "inbox", "upnext"];

// Looked up rather than written out a second time, and filtered rather than
// asserted non-null: ViewId also names destinations SECTIONS deliberately does
// not carry ("channel", "share"), so a plausible future edit to TAB_IDS
// type-checks and finds nothing. A missing id then costs the bar one tab; the
// non-null assertion it replaces cost the whole phone UI, because TabBar throws
// reading `.id` off undefined while rendering.
export const TAB_ITEMS: NavItem[] = TAB_IDS.map((id) =>
  ALL_NAV_ITEMS.find((i) => i.id === id),
).filter((i): i is NavItem => i !== undefined);

export const MORE_ITEMS: NavItem[] = ALL_NAV_ITEMS.filter(
  (i) => !TAB_IDS.includes(i.id),
);
