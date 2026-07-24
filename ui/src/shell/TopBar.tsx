import { Icon } from "../icons";

// TopBar — the sticky header per the mockup's `.topbar`: a serif page title
// (with a small mono subtitle, e.g. "128 videos · 41 GB") plus the search
// field. Search is a plain controlled input; App owns the query state and
// wires it to the two list views that have a search box — the Library and the
// Channels list — passing each its own query and placeholder. The channel
// *detail* page keeps its own separate in-page search box, since the top bar
// isn't detail-aware.
export function TopBar({
  title,
  subtitle,
  search,
  onSearchChange,
  searchPlaceholder = "Search titles",
  showSearch = true,
}: {
  title: string;
  subtitle?: string;
  search?: string;
  onSearchChange?: (value: string) => void;
  searchPlaceholder?: string;
  showSearch?: boolean;
}) {
  return (
    <div className="topbar">
      <h1>
        {title}
        {subtitle ? <em>{subtitle}</em> : null}
      </h1>
      {showSearch ? (
        <div className="topbar-search">
          <Icon name="search" size="16px" />
          <input
            placeholder={searchPlaceholder}
            value={search ?? ""}
            onChange={(e) => onSearchChange?.(e.target.value)}
          />
        </div>
      ) : null}
    </div>
  );
}
