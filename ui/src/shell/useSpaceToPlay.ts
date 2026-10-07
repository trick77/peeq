import { useEffect } from "react";
import { togglePlayback, visibleVideo } from "../videoHost";

// Where Space already means something: typing it, pressing the focused control
// with it, or the <video>'s own native controls. Space on any of these is left
// to the browser, so keyboard navigation keeps working.
const KEEPS_SPACE =
  'input, textarea, select, button, a[href], video, audio, [contenteditable]:not([contenteditable="false"]), [role="button"], [role="link"], [role="slider"], [role="checkbox"], [role="switch"], [role="tab"], [role="menuitem"], [role="option"]';

// useSpaceToPlay makes Space play and pause the video wherever it is on screen:
// the player page's stage or the now-playing dock. Mounted once, in App.
//
// It drives the same element the dock's button does (see togglePlayback), so
// resume tracking, the sleep timer and SponsorBlock auto-skip stay correct.
// With nothing on screen — no video, or one stopped and parked out of sight —
// Space keeps scrolling the page.
export function useSpaceToPlay() {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.code !== "Space" || e.repeat) return;
      if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.defaultPrevented) return;
      const target = e.target;
      if (target instanceof Element && target.closest(KEEPS_SPACE)) return;
      const el = visibleVideo();
      if (!el) return;
      e.preventDefault();
      togglePlayback(el);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}
