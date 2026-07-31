import { useLayoutEffect, useState, type RefObject } from "react";

// Every anchored menu in peeq (.sleepmenu, .catmenu, .rowmenu) is a plain
// absolutely-positioned child of its trigger's wrapper, opening downward with
// no idea where the viewport ends. That is fine on a tall desktop window and
// wrong everywhere else: on an iPad the player's action row sits ~100px above
// the fold, so the sleep menu opened straight off the bottom of the screen.
//
// This measures once per open and reports which way there is room for. It is
// deliberately not a floating-ui-grade positioner — the menus are small, the
// trigger cannot move while the menu is open (an outside mousedown closes it),
// and the only axis that ever ran out is the vertical one.
//
// Returns "down" when it cannot decide, so anything unmeasurable keeps
// today's behaviour: jsdom reports every rect as zero, which is also why the
// flip has no unit test — it is verified in a real browser.
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
    // offsetHeight, not the rect: the menu is measured while still placed
    // downward, and this is the height it wants regardless of where it lands.
    const wanted = menu.offsetHeight;
    if (!wanted) return;

    // GAP mirrors the CSS offset from the trigger; EDGE keeps the menu off the
    // very edge of the screen.
    const GAP = 7;
    const EDGE = 8;
    const below = window.innerHeight - rect.bottom - GAP - EDGE;
    const above = rect.top - GAP - EDGE;

    // Flip only when down genuinely does not fit AND up has more room. A menu
    // too tall for either side will scroll against the max-height clamp
    // whatever happens, so it goes to the roomier side to show the most rows.
    setPlace(wanted > below && above > below ? "up" : "down");
  }, [open, triggerRef, menuRef]);

  return place;
}
