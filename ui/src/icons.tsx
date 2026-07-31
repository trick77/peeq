import type { CSSProperties } from "react";
import {
  Library,
  CirclePlay,
  Plus,
  Clock,
  Inbox,
  Settings,
  Search,
  Sparkles,
  Star,
  Check,
  Trash2,
  Link2,
  Download,
  TriangleAlert,
  AlignLeft,
  SkipForward,
  ExternalLink,
  ListTree,
  ClockArrowUp,
  History,
  Play,
  Tv,
  Captions,
  ChevronRight,
  ChevronDown,
  ChevronLeft,
  BadgeCheck,
  RefreshCw,
  Copy,
  LoaderCircle,
  EllipsisVertical,
  Ellipsis,
  PanelLeft,
  Share2,
  type LucideIcon,
} from "lucide-react";

/**
 * Icon — thin wrapper over lucide-react (ISC-licensed, self-contained SVG),
 * ported from ../music's Icon.tsx pattern. Every icon the approved mockup
 * uses (rail nav, dock, cookie status, add/player/settings placeholders)
 * gets a name here; more names are added as Task 14 views need them.
 *
 * Usage:
 *   <Icon name="library" />
 *   <Icon name="star" size="16px" label="Favorite" />
 */
const COMPONENTS = {
  library: Library,
  circlePlay: CirclePlay,
  plus: Plus,
  clock: Clock,
  inbox: Inbox,
  settings: Settings,
  search: Search,
  sparkles: Sparkles,
  star: Star,
  starFilled: Star, // rendered solid via the FILLED set below
  check: Check,
  trash: Trash2,
  link: Link2,
  download: Download,
  warning: TriangleAlert,
  alignLeft: AlignLeft,
  skipForward: SkipForward,
  externalLink: ExternalLink,
  listTree: ListTree,
  // The two Collect destinations that face opposite directions in time: a clock
  // pointing forward for what peeq is about to do, a clock winding back for what
  // it already did. The pair reads as a pair, which "download"/"list" did not.
  clockArrowUp: ClockArrowUp,
  history: History,
  play: Play,
  tv: Tv,
  // Rendered solid via the FILLED set below. Nothing draws it at the moment:
  // its last consumer was the rail's logo, which is gone — the rail now wears
  // the wordmark alone. The one lockup still standing, the share page's footer
  // (PeeqMark in views/Share.tsx), sets a magnifier in a gradient tile and
  // never asked for this glyph. Kept rather than deleted — a filled play is the
  // obvious glyph for the next play affordance, and the entry is one line.
  playFilled: Play,
  captions: Captions,
  chevronRight: ChevronRight,
  chevronDown: ChevronDown,
  chevronLeft: ChevronLeft,
  verified: BadgeCheck, // YouTube's channel checkmark, not a peeq state
  refresh: RefreshCw,
  copy: Copy,
  share: Share2, // share-link action (player action row + share popover)
  moreVertical: EllipsisVertical, // the row's 3-dot actions trigger
  // The same glyph in both directions, as loom's sidebar button has: a chevron
  // that flips says "this moves left/right", but the control is a switch
  // between two layouts, and its aria-label already says which way it goes.
  panelLeft: PanelLeft, // rail collapse/expand toggle
  more: Ellipsis, // the phone tab bar's fifth tab, holding what does not fit
  spinner: LoaderCircle, // spun by .ui-spin — every async wait spins
} satisfies Record<string, LucideIcon>;

export type IconName = keyof typeof COMPONENTS;

/** Names rendered as a solid (filled) glyph rather than an outline. */
const FILLED = new Set<IconName>(["starFilled", "playFilled"]);

export function Icon({
  name,
  size = "1.15rem",
  label,
  style,
}: {
  name: IconName;
  /** font-size-equivalent of the glyph (px/rem string). */
  size?: string;
  /** When set, the icon is meaningful (role=img); otherwise it is decorative. */
  label?: string;
  style?: CSSProperties;
}) {
  const C = COMPONENTS[name];
  return (
    <C
      size={size}
      strokeWidth={1.9}
      fill={FILLED.has(name) ? "currentColor" : "none"}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      style={{
        display: "inline-block",
        flexShrink: 0,
        verticalAlign: "middle",
        ...style,
      }}
    />
  );
}
