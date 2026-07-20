// Mirrors backend/internal/videos/category.go (the authority). Colors are a
// muted scanning aid used only as small dots; ai uses the warm accent, the
// fallback uses --color-faint. Keep this list in sync with the Go enum.
export type CategoryMeta = { id: string; label: string; color: string };

export const UNCATEGORIZED = "uncategorized";

export const CATEGORIES: CategoryMeta[] = [
  { id: "ai", label: "AI", color: "#d97757" },
  { id: "tech", label: "Technology & Gadgets", color: "#5aa0c8" },
  { id: "software", label: "Software & Programming", color: "#7c9cff" },
  { id: "science", label: "Science & Research", color: "#5ac89a" },
  { id: "space", label: "Space & Astronomy", color: "#9c7cdc" },
  { id: "engineering", label: "Engineering & Making", color: "#d6a15a" },
  { id: "business", label: "Business & Finance", color: "#7cc86a" },
  { id: "news", label: "News & Current Events", color: "#c8607a" },
  { id: "history", label: "History & Culture", color: "#c89a5a" },
  { id: "health", label: "Health & Medicine", color: "#6ac8b4" },
  { id: "nature", label: "Nature & Environment", color: "#6aa86a" },
  { id: "education", label: "Education & Tutorials", color: "#8a9ac8" },
  { id: "gaming", label: "Gaming", color: "#b06adc" },
  { id: "entertainment", label: "Entertainment & Music", color: "#dc6a9c" },
  { id: "uncategorized", label: "Uncategorized", color: "#6f6d66" },
];

export const CATEGORY_BY_ID: Record<string, CategoryMeta> = Object.fromEntries(
  CATEGORIES.map((c) => [c.id, c]),
);
