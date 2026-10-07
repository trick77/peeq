import { useEffect } from "react";
import { overlayOwnsKeys } from "../shortcuts";
import { togglePlayback, visibleVideo } from "../videoHost";

// useSpaceToPlay makes Space play and pause the video wherever it is on screen:
// the player page's stage or the now-playing dock. Mounted once, in App.
//
// It acts only when nothing has focus — keydown lands on <body>. Anything that
// can be focused already has a use for Space: typing it, pressing a button or
// link, opening a <summary>, the video's own native controls, a scroller or a
// menu. A list of those would be one bug per element it forgot, so the rule is
// the general one. In Safari a clicked button does not take focus, so after a
// click Space still reaches the video; in Chrome it presses the clicked button
// again until focus moves off it.
//
// It drives the same element the dock's button does (see togglePlayback), so
// resume tracking, the sleep timer and SponsorBlock auto-skip stay correct.
// With nothing on screen — no video, or one stopped and parked out of sight —
// or an overlay open, Space does what it did before.
export function useSpaceToPlay() {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // By the character, as "/" and Escape are: a virtual keyboard can send
      // key " " with no physical code.
      if (e.key !== " " || e.repeat) return;
      if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.defaultPrevented) return;
      if (e.target !== document.body && e.target !== document.documentElement)
        return;
      if (overlayOwnsKeys(e.target)) return;
      // Stricter than "/": ANY open menu or popover stands Space down, not
      // only one holding focus. A click on a menu's padding drops focus to
      // <body> with the menu still open, and toggling the video behind it
      // reads as the menu having done something. Every one of them renders
      // only while open, so presence is the test.
      if (document.querySelector('[role="menu"],[role="dialog"]')) return;
      // Fullscreen, the browser's own media controls answer Space already;
      // taking it as well toggles twice, which looks like nothing happened.
      if (document.fullscreenElement) return;
      const el = visibleVideo();
      if (!el) return;
      e.preventDefault();
      togglePlayback(el);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}
