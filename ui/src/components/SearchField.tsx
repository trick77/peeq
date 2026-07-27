import { useEffect, useRef, type CSSProperties } from "react";
import { Icon } from "../icons";
import { controlClass } from "../ui";

// SearchField — the one search box for a list page.
//
// It replaces the old sticky top bar (shell/SearchBar), which parked a lone
// 260px pill at the far right of an otherwise empty strip: the furthest point
// on the page from the filter chips and the results it acted on. The field now
// sits at the left of the same `.listbar` row as the sort control, directly
// above the list — the layout the channel page's Archive tab already used.
//
// The `/` hint is not decoration: the key really does focus the field, from
// anywhere on the page. The hint hides once the field is focused or has text,
// so it never fights the browser's own clear button for the same corner.
export function SearchField({
  value,
  onChange,
  placeholder,
  label,
  maxWidth = 600,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  // label is the accessible name. It is a separate prop from placeholder
  // because a placeholder alone is not an accessible name — the old top-bar
  // field had no name at all.
  label: string;
  maxWidth?: number;
}) {
  const ref = useRef<HTMLInputElement>(null);

  // "/" focuses the field. Bail whenever the keystroke is already going
  // somewhere that wants it — another field, a contenteditable, or a chord
  // like ⌘/ that belongs to the browser — otherwise typing a URL on the Add
  // page would yank focus across the screen mid-word.
  useEffect(() => {
    function handleKey(e: KeyboardEvent) {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
      const target = e.target as HTMLElement | null;
      if (
        target &&
        (target.isContentEditable ||
          target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.tagName === "SELECT")
      ) {
        return;
      }
      // An open overlay owns the page. None of them trap focus, and they all
      // park it on a *button* — ConfirmDialog on Cancel, RowMenu and
      // CategoryPicker on their first item — which the tag check above waves
      // through. Without this, "/" during a delete confirmation pulls focus to
      // a field behind the scrim and leaves the still-open dialog with nothing
      // focused inside it.
      //
      // The modal case is asked of the document, not of the event target: a
      // scrim click can leave focus on <body>, which no ancestor lookup from
      // the target would catch. Popovers are asked of the target instead —
      // they are not modal, so one being open somewhere on the page is no
      // reason to ignore a keystroke aimed outside it.
      if (document.querySelector('[role="dialog"][aria-modal="true"]')) return;
      if (target?.closest('[role="menu"],[role="dialog"]')) return;
      // Without this the slash lands in the field we just focused.
      e.preventDefault();
      ref.current?.focus();
    }
    window.addEventListener("keydown", handleKey);
    return () => window.removeEventListener("keydown", handleKey);
  }, []);

  return (
    // maxWidth rides in as a custom property rather than an inline max-width so
    // the narrow-viewport rule in index.css can still widen the field — an
    // inline value would out-specify any stylesheet.
    <div
      className="searchfield"
      style={{ "--searchfield-max": `${maxWidth}px` } as CSSProperties}
    >
      <Icon name="search" size="15px" />
      <input
        ref={ref}
        className={controlClass}
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={label}
      />
      <kbd aria-hidden="true">/</kbd>
    </div>
  );
}
