import { Fragment, useEffect, useRef, useState } from "react";
import { Icon, type IconName } from "../icons";
import { useMenuPlacement } from "./useMenuPlacement";

export type RowMenuAction = {
  label: string;
  icon: IconName;
  // onClick fires for button items (callbacks). Omit it for href items.
  onClick?: () => void;
  // href renders the item as a link instead of a button — for actions that are
  // navigations rather than callbacks (open a page, save a file). newTab opens
  // it in a new tab (external links); download hints the browser to save the
  // target rather than navigate to it.
  href?: string;
  newTab?: boolean;
  download?: boolean;
  // flag is a short trailing tag (e.g. "failed") marking an item that needs
  // attention; it pairs with RowMenu's `attention` dot on the trigger.
  flag?: string;
  // danger renders the item in the destructive style (muted red, solid-red
  // hover) — for the one irreversible entry, Delete.
  danger?: boolean;
};

// RowMenu — a compact "⋮" actions popover, the loom pattern rebuilt on the
// mechanics peeq already solved in CategoryPicker: it closes on an outside
// click or Escape (Escape returns focus to the trigger), rove-focuses with the
// arrow keys, and marks itself up as a real menu. The popover is right-aligned
// so it hangs under the trigger at a row's right edge without running off.
//
// The trigger's visibility (quiet-until-hover on desktop, always shown on
// touch) is a concern of the row's CSS, not this component — it only sets
// aria-expanded, which that CSS keys off to keep an open menu's dots visible.
// `attention` renders a small dot on the trigger, so a menu holding an item
// that needs action (a failed step) reads at a glance without being opened.
//
// A destructive item is always fenced off by a hairline separator, so Delete
// can never be hit by a slip of the pointer aimed at the item above it. The
// rule lives here rather than in each caller's action list: callers just mark
// the item `danger` and the divider follows automatically.
export function RowMenu({
  actions,
  label = "Actions",
  attention = false,
}: {
  actions: RowMenuAction[];
  label?: string;
  attention?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const place = useMenuPlacement(open, triggerRef, menuRef);

  // Pull focus into the menu when it opens, so the keyboard lands inside it.
  //
  // The MENU takes focus, not its first item. Focusing the first item drew the
  // global :focus-visible ring — 2px of --color-accent — around whatever
  // happens to be at the top of the list, every time the menu opened, mouse or
  // keyboard. On the Player that is "Share…", so opening the ⋮ framed the
  // share entry in terracotta and made it look flagged.
  //
  // A container with tabindex=-1 carries the same keyboard entry — Escape
  // closes, ArrowDown/ArrowUp step through the items (see onMenuKeyDown, which
  // starts from the top when focus is still on the menu itself), Tab walks in
  // — and wears no ring.
  useEffect(() => {
    if (!open) return;
    menuRef.current?.focus();
  }, [open]);

  // Close on an outside click or Escape. Escape returns focus to the trigger;
  // an outside click does not, since the click already moved focus somewhere
  // the user chose.
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
      menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [],
    );
    if (items.length === 0) return;
    // -1 when focus is still on the menu container itself, which is the state
    // it opens in. ArrowDown then lands on the first item, which (i + 1) gives
    // for free; ArrowUp has to be spelled out, because (i - 1 + len) % len
    // would hand back the second-to-last item rather than the last.
    const i = items.indexOf(document.activeElement as HTMLElement);
    const next =
      e.key === "ArrowDown"
        ? items[(i + 1) % items.length]
        : i < 0
          ? items[items.length - 1]
          : items[(i - 1 + items.length) % items.length];
    next.focus();
  }

  // Index of the first destructive item — the separator goes right above it.
  // -1 (no danger item) and 0 (the menu opens with it) both mean no divider.
  const firstDanger = actions.findIndex((a) => a.danger);

  function pick(action: RowMenuAction) {
    setOpen(false);
    triggerRef.current?.focus();
    action.onClick?.();
  }

  return (
    <span className="menuwrap" ref={wrapRef}>
      <button
        type="button"
        ref={triggerRef}
        className="kebab"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="moreVertical" size="18px" />
        {attention ? <span className="kebab-dot" aria-hidden="true" /> : null}
      </button>
      {open ? (
        <div
          className="rowmenu"
          role="menu"
          /* Focusable only programmatically — the menu itself takes focus when
             it opens, never an item inside it. */
          tabIndex={-1}
          data-place={place}
          ref={menuRef}
          onKeyDown={onMenuKeyDown}
        >
          {actions.map((a, i) => (
            <Fragment key={a.label}>
              {i === firstDanger && i > 0 ? (
                <div className="rowmenu-sep" role="separator" />
              ) : null}
              {a.href ? (
                <a
                  role="menuitem"
                  className={a.danger ? "danger" : undefined}
                  href={a.href}
                  target={a.newTab ? "_blank" : undefined}
                  rel={a.newTab ? "noreferrer" : undefined}
                  download={a.download ? "" : undefined}
                  onClick={() => pick(a)}
                >
                  <Icon name={a.icon} size="16px" />
                  {a.label}
                  {a.flag ? <span className="mi-flag">{a.flag}</span> : null}
                </a>
              ) : (
                <button
                  type="button"
                  role="menuitem"
                  className={a.danger ? "danger" : undefined}
                  onClick={() => pick(a)}
                >
                  <Icon name={a.icon} size="16px" />
                  {a.label}
                  {a.flag ? <span className="mi-flag">{a.flag}</span> : null}
                </button>
              )}
            </Fragment>
          ))}
        </div>
      ) : null}
    </span>
  );
}
