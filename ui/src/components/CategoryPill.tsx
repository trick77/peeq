import type { CategoryMeta } from "../categories";

// CategoryPill is the poster's category chip: the category's dot and a label,
// on the cover scrim. The Library card shows the category, the Inbox card the
// category with its primary subcategory; one component keeps the two chips
// the same markup. Decorative for a screen reader: the card around it already
// names the video.
export function CategoryPill({
  meta,
  text = meta.label,
  title,
  className,
}: {
  meta: CategoryMeta;
  text?: string;
  title?: string;
  className?: string;
}) {
  return (
    <span
      className={`metapill oncover${className ? ` ${className}` : ""}`}
      title={title}
      aria-hidden="true"
    >
      <span className="dotc" style={{ background: meta.color }} />
      <span className="metapill-text">{text}</span>
    </span>
  );
}
