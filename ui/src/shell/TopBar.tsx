import { Icon } from "../icons";

// TopBar — the sticky header per the mockup's `.topbar`: a serif page title
// (with a small mono subtitle, e.g. "128 videos · 41 GB") plus the search
// field. Search is a plain controlled input here; wiring it to a real
// query is Task 14's job (Library view).
export function TopBar({
  title,
  subtitle,
  search,
  onSearchChange,
  searchPlaceholder = "Search titles & subtitles",
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
