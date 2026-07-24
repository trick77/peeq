// Mirrors backend/internal/videos/category.go (the authority) on id and order.
// The labels here are the SHORT display forms shown on the category pills — the
// lead noun of each backend label, to keep the pills narrow. The backend keeps
// the fuller "A & B" labels, which the classifier matches its replies against,
// so `id` (unchanged) is the sync key between the two, not the label text. The
// Go enum's Hint field (prompt steering) is not shown and not mirrored. Colors
// are a muted scanning aid used only as small dots; ai uses the warm accent,
// the fallback uses --color-faint.
export type CategoryMeta = { id: string; label: string; color: string };

export const UNCATEGORIZED = "uncategorized";

export const CATEGORIES: CategoryMeta[] = [
  { id: "ai", label: "AI", color: "#d97757" },
  { id: "tech", label: "Tech", color: "#5aa0c8" },
  { id: "software", label: "Software", color: "#7c9cff" },
  { id: "science", label: "Science", color: "#5ac89a" },
  { id: "space", label: "Space", color: "#9c7cdc" },
  { id: "engineering", label: "Engineering", color: "#d6a15a" },
  { id: "business", label: "Business", color: "#7cc86a" },
  { id: "news", label: "News", color: "#c8607a" },
  { id: "politics", label: "Politics", color: "#a05a7c" },
  { id: "history", label: "History", color: "#c89a5a" },
  { id: "health", label: "Health", color: "#6ac8b4" },
  { id: "sports", label: "Sports", color: "#c8c85a" },
  { id: "food", label: "Food", color: "#c85a5a" },
  { id: "nature", label: "Nature", color: "#6aa86a" },
  { id: "travel", label: "Travel", color: "#5ac8c8" },
  { id: "automotive", label: "Automotive", color: "#8c6a4a" },
  { id: "home", label: "Home", color: "#c8a08a" },
  { id: "education", label: "Education", color: "#8a9ac8" },
  { id: "arts", label: "Arts", color: "#dca0a0" },
  { id: "music", label: "Music", color: "#dc6adc" },
  { id: "gaming", label: "Gaming", color: "#b06adc" },
  { id: "entertainment", label: "Entertainment", color: "#dc6a9c" },
  { id: "uncategorized", label: "Uncategorized", color: "#6f6d66" },
];

export const CATEGORY_BY_ID: Record<string, CategoryMeta> = Object.fromEntries(
  CATEGORIES.map((c) => [c.id, c]),
);
