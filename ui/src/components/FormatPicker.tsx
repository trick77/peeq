import { useEffect, useRef, useState } from "react";
import { Icon } from "../icons";
import { controlClass } from "../ui";
import { PRESETS_NO_CUSTOM, presetLabel } from "../formatPresets";
import { useMenuPlacement } from "./useMenuPlacement";

type Props = {
  // The channel's stored format_override: "" for none, a preset id, or —
  // for rows written before this picker existed — a raw yt-dlp selector.
  value: string;
  // The global settings' format_preset, marked DEFAULT in the list. Pass ""
  // while settings are still loading, or when the global preset is "custom";
  // either way no row is badged, which is better than badging a wrong one.
  globalPreset: string;
  onPick: (value: string) => void;
  disabled?: boolean;
};

// FormatPicker is a channel's Format override control. It deliberately does
// NOT offer "custom": a hand-written yt-dlp selector is a global setting,
// set once in Settings, not something to retype per channel.
//
// It is a menu rather than a <select> for one reason — the DEFAULT badge.
// Marking which preset the global setting resolves to is the whole point of
// the control (otherwise "use the global setting" says nothing about what
// will actually be downloaded), and an <option> can't carry a badge.
//
// The open/close behaviour is CategoryPicker's, down to the detail that
// Escape returns focus to the trigger while an outside click deliberately
// does not — the click has already moved focus somewhere the user chose.
export function FormatPicker({ value, globalPreset, onPick, disabled }: Props) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const place = useMenuPlacement(open, triggerRef, menuRef);

  // A value that is neither empty nor a preset id is a selector someone
  // typed before this control existed. It is shown as-is and never
  // rewritten on load: silently resetting it would change what the channel
  // downloads without the user asking. Any pick replaces it.
  const known = value === "" ? null : presetLabel(value);
  const legacy = value !== "" && known === null;
  const triggerLabel =
    value === "" ? "Use the global setting" : (known ?? value);

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
    if (id !== value) onPick(id);
  }

  return (
    <span className="fmtwrap" ref={wrapRef}>
      <button
        type="button"
        ref={triggerRef}
        className={`${controlClass} fmtpick${legacy ? " legacy" : ""}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Format override: ${triggerLabel}. Change it`}
        disabled={disabled}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="fmtpick-val">{triggerLabel}</span>
        <Icon name="chevronDown" size="14px" />
      </button>
      {open ? (
        <div
          className="fmtmenu"
          role="menu"
          data-place={place}
          ref={menuRef}
          onKeyDown={onMenuKeyDown}
        >
          <button
            type="button"
            role="menuitemradio"
            aria-checked={value === ""}
            onClick={() => pick("")}
          >
            <span className="fmtmenu-label">Use the global setting</span>
          </button>
          {/* role="separator" because a role="menu" only admits menuitem,
              group and separator children; an unroled span is a text node a
              screen reader announces in the middle of the list. */}
          <span className="fmtmenu-sep" role="separator" />
          {PRESETS_NO_CUSTOM.map((p) => (
            <button
              key={p.id}
              type="button"
              role="menuitemradio"
              aria-checked={p.id === value}
              onClick={() => pick(p.id)}
            >
              <span className="fmtmenu-label">{p.label}</span>
              {p.id === globalPreset ? (
                <span className="fmtmenu-tag">Default</span>
              ) : null}
            </button>
          ))}
        </div>
      ) : null}
    </span>
  );
}
