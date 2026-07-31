import { useLayoutEffect, useState, type RefObject } from "react";

// Every anchored menu in peeq (.sleepmenu, .catmenu, .fmtmenu, .rowmenu) is a
// plain absolutely-positioned child of its trigger's wrapper, opening downward
// with no idea where the viewport ends. That is fine on a tall desktop and
// wrong everywhere else: on an iPad the player's action row sits ~100px above
// the fold, so the sleep menu opened straight off the bottom of the screen.
//
// This measures once per open and reports which way there is room for. It is
// deliberately not a floating-ui-grade positioner — the menus are small, the
// trigger cannot move while the menu is open (an outside mousedown closes it),
// and the only axis that ever ran out is the vertical one.
//
// Scrolling with a menu open is the one case it does not re-answer: the menu
// stays glued to its trigger, so it never detaches, but a menu flipped up can
// be scrolled off the top. Re-measuring on scroll would mean a listener per
// open menu to fix a case that needs a deliberate two-finger scroll on a page
// whose menu closes on the next tap anyway.
//
// Returns "down" when it cannot decide, so anything unmeasurable keeps
// today's behaviour. jsdom reports every rect as zero, so the tests feed the
// geometry in rather than laying anything out — they cover this decision, not
// the two CSS properties that act on it.
export function useMenuPlacement(
  open: boolean,
  triggerRef: RefObject<HTMLElement | null>,
  menuRef: RefObject<HTMLElement | null>,
): "down" | "up" {
  const [place, setPlace] = useState<"down" | "up">("down");

  // Layout effect, not effect: the menu must not paint downward for a frame
  // before jumping up.
  useLayoutEffect(() => {
    if (!open) {
      setPlace("down");
      return;
    }
    const trigger = triggerRef.current;
    const menu = menuRef.current;
    if (!trigger || !menu) return;
    const rect = trigger.getBoundingClientRect();
    // offsetHeight is the height after max-height has clamped it, which is
    // the right input: a menu already scrolling at its clamp overflows nothing
    // wherever it lands, so scrollHeight would flip menus that did not need it.
    const wanted = menu.offsetHeight;
    if (!wanted) return;

    // GAP mirrors the CSS offset from the trigger; EDGE keeps the menu off the
    // very edge of the screen.
    const GAP = 7;
    const EDGE = 8;
    // The floor is the tab bar's top edge when there is one, not the viewport's
    // bottom: on a phone the bar is fixed over the last ~57px and paints above
    // these menus (z-index 40 vs 30), so a menu measured against innerHeight
    // opens "down" into rows that are covered — and a tap meant for one lands
    // on the bar and navigates away. Absent (every width above the breakpoint,
    // and jsdom) the answer is the viewport, exactly as before.
    const bar = document.querySelector(".tabbar");
    const floor = bar ? bar.getBoundingClientRect().top : window.innerHeight;
    const below = floor - rect.bottom - GAP - EDGE;
    const above = rect.top - GAP - EDGE;

    // Flip only when down genuinely does not fit AND up has more room. A menu
    // too tall for either side will scroll against the max-height clamp
    // whatever happens, so it goes to the roomier side to show the most rows.
    setPlace(wanted > below && above > below ? "up" : "down");
  }, [open, triggerRef, menuRef]);

  return place;
}
