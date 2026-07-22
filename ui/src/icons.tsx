import type { CSSProperties } from "react";
import {
  Library,
  CirclePlay,
  Plus,
  Clock,
  Settings,
  Search,
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
  Play,
  Tv,
  Captions,
  ChevronRight,
  ChevronDown,
  RefreshCw,
  Copy,
  LoaderCircle,
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
  settings: Settings,
  search: Search,
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
  play: Play,
  tv: Tv,
  playFilled: Play, // rendered solid via the FILLED set below — rail logo + player play button
  captions: Captions,
  chevronRight: ChevronRight,
  chevronDown: ChevronDown,
  refresh: RefreshCw,
  copy: Copy,
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
