import { useEffect, useRef, useState } from "react";
import { Icon } from "../icons";
import { CATEGORIES, CATEGORY_BY_ID, UNCATEGORIZED } from "../categories";
import { useMenuPlacement } from "./useMenuPlacement";

type Props = {
  category: string;
  onPick: (category: string) => void;
};

// CategoryPicker is the Player's editable category pill. It wears the same
// .metapill the Library card uses, so the same fact reads the same in both
// places; the caret and the hover state are what say this one is operable.
//
// An uncategorized video still renders a pill here (the card hides it): the
// Player is the one place the category can be corrected, and a video with no
// transcript can never get one any other way.
//
// There is no "clear" entry. Handing a video back to the classifier is not a
// thing the user asked for, and 'uncategorized' is a state the app assigns,
// never one worth choosing on purpose.
export function CategoryPicker({ category, onPick }: Props) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const place = useMenuPlacement(open, triggerRef, menuRef);

  const meta =
    category && category !== UNCATEGORIZED
      ? (CATEGORY_BY_ID[category] ?? null)
      : null;

  // Focus the checked row when the menu opens, so the keyboard lands where
  // the eye does.
  useEffect(() => {
    if (!open) return;
    const menu = menuRef.current;
    if (!menu) return;
    const items = menu.querySelectorAll<HTMLButtonElement>("button");
    const checked = menu.querySelector<HTMLButtonElement>(
      'button[aria-checked="true"]',
    );
    (checked ?? items[0])?.focus();
  }, [open]);

  // Close on an outside click or Escape. Escape returns focus to the
  // trigger; an outside click deliberately does not, since the click has
  // already moved focus somewhere the user chose.
  useEffect(() => {
    if (!open) return;
    function onDocClick(e: MouseEvent) {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        setOpen(false);
        triggerRef.current?.focus();
      }
    }
    document.addEventListener("mousedown", onDocClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDocClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  function onMenuKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault();
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? [],
    );
    if (items.length === 0) return;
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    const next =
      e.key === "ArrowDown"
        ? items[(i + 1) % items.length]
        : items[(i - 1 + items.length) % items.length];
    next.focus();
  }

  function pick(id: string) {
    setOpen(false);
    triggerRef.current?.focus();
    if (id !== category) onPick(id);
  }

  return (
    <span className="catwrap" ref={wrapRef}>
      <button
        type="button"
        ref={triggerRef}
        className={meta ? "metapill catpick" : "metapill catpick unset"}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={
          meta ? `Category: ${meta.label}. Change it` : "No category. Set one"
        }
        onClick={() => setOpen((v) => !v)}
      >
        {meta ? (
          <span className="dotc" style={{ background: meta.color }} />
        ) : null}
        {meta ? meta.label : "Uncategorized"}
        <Icon name="chevronDown" size="13px" />
      </button>
      {open ? (
        <div
          className="catmenu"
          role="menu"
          data-place={place}
          ref={menuRef}
          onKeyDown={onMenuKeyDown}
        >
          {CATEGORIES.filter((c) => c.id !== UNCATEGORIZED).map((c) => (
            <button
              key={c.id}
              type="button"
              role="menuitemradio"
              aria-checked={c.id === category}
              onClick={() => pick(c.id)}
            >
              <span className="dotc" style={{ background: c.color }} />
              {c.label}
            </button>
          ))}
        </div>
      ) : null}
    </span>
  );
}
