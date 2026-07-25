import { Icon } from "../icons";

// SearchBar — the sticky bar above the page. It used to carry a serif page
// title too, but the title only ever repeated the rail item you had just
// clicked, so it was dropped and the search field is all that remains. App
// renders this bar only on the two list views that have a search box — the
// Library and the Channels list — and no bar at all elsewhere, rather than
// leaving an empty bordered strip. Search is a plain controlled input; App
// owns the query state and passes each view its own query and placeholder.
// The channel *detail* page keeps its own separate in-page search box, since
// this bar isn't detail-aware.
export function SearchBar({
  search,
  onSearchChange,
  searchPlaceholder = "Search titles",
}: {
  search?: string;
  onSearchChange?: (value: string) => void;
  searchPlaceholder?: string;
}) {
  return (
    <div className="topbar">
      <div className="topbar-search">
        <Icon name="search" size="16px" />
        <input
          placeholder={searchPlaceholder}
          value={search ?? ""}
          onChange={(e) => onSearchChange?.(e.target.value)}
        />
      </div>
    </div>
  );
}
