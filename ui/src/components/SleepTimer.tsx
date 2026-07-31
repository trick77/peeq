import { useEffect, useRef, useState } from "react";
import { Icon } from "../icons";
import { formatDuration } from "../format";
import { useMenuPlacement } from "./useMenuPlacement";

export const SLEEP_PRESETS = [5, 10, 15, 30, 60] as const;

type Props = {
  // Seconds left, or null when the timer is off. The Player owns the budget;
  // this component only draws it.
  remainingSeconds: number | null;
  // Which preset is armed, so the menu's radio state is honest. Derivable
  // from remainingSeconds only at the instant of arming, which is why it
  // travels separately.
  armedMinutes: number | null;
  // Minutes to arm, or null to cancel.
  onArm: (minutes: number | null) => void;
};

// SleepTimer is the Player's "stop playing in N minutes" pill — the control
// for watching in bed. It wears the same .metapill as the category picker at
// the other end of the row, for the same reason that one does: a control
// hanging off the action row should read as one of the row's controls.
//
// The pill is rendered armed or not. An affordance that only appears on
// hover is one a touch user can never find, and "off" is a state worth
// naming rather than hiding. Disarmed it reads "Sleep"; armed it carries the
// live countdown, which is the whole point — a promise you can check without
// opening anything.
//
// The budget itself lives in Player, not here. Expiry has to pause the
// <video>, persist the resume position and raise a toast, all of which are
// Player's; passing three refs down to reach them would be the tail wagging
// the dog.
export function SleepTimer({ remainingSeconds, armedMinutes, onArm }: Props) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const place = useMenuPlacement(open, triggerRef, menuRef);

  const armed = remainingSeconds !== null;

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

  // No same-value short-circuit, unlike CategoryPicker: re-picking the preset
  // that is already running is a legitimate restart, and swallowing it would
  // look broken.
  function pick(minutes: number | null) {
    setOpen(false);
    triggerRef.current?.focus();
    onArm(minutes);
  }

  const left = armed ? formatDuration(remainingSeconds) : null;

  return (
    <span className="sleepwrap" ref={wrapRef}>
      <button
        type="button"
        ref={triggerRef}
        className={armed ? "metapill sleeppick armed" : "metapill sleeppick"}
        aria-haspopup="menu"
        aria-expanded={open}
        // The label carries the state without colour, as the captions toggle
        // beside it does. It names the armed preset rather than the live
        // countdown, and that is not cosmetic: pick() returns focus to this
        // button, and a screen reader re-announces the accessible name of the
        // focused element whenever it changes — a countdown in here would be
        // read out once a second, which is exactly what skipping aria-live was
        // meant to avoid. The preset is stable for as long as the timer runs;
        // the countdown stays visible and in the title, and the toast speaks
        // at both ends.
        aria-label={
          armed
            ? `Sleep timer: ${armedMinutes} minutes. Change it`
            : "Sleep timer off"
        }
        title={armed ? `Sleep timer: ${left} left` : "Sleep timer"}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="clock" size="15px" />
        {armed ? <span className="sleepleft">{left}</span> : "Sleep"}
        <Icon name="chevronDown" size="13px" />
      </button>
      {open ? (
        <div
          className="sleepmenu"
          role="menu"
          data-place={place}
          ref={menuRef}
          onKeyDown={onMenuKeyDown}
        >
          {/* Off leads, so cancelling is the first thing the keyboard and the
              eye reach — a timer you can't call off is worse than no timer. */}
          <button
            type="button"
            role="menuitemradio"
            aria-checked={!armed}
            onClick={() => pick(null)}
          >
            Off
          </button>
          {SLEEP_PRESETS.map((m) => (
            <button
              key={m}
              type="button"
              role="menuitemradio"
              aria-checked={armed && m === armedMinutes}
              onClick={() => pick(m)}
            >
              {m} minutes
            </button>
          ))}
        </div>
      ) : null}
    </span>
  );
}
