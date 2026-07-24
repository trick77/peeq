// route — a small hand-rolled History-API router. peeq deliberately runs no
// router library (App.tsx's manual view-state, matching loom's pattern for an
// app this size); this keeps that convention while making the URL the single
// source of truth for which top-level page is open, so a page can be deep-
// linked, refreshed, and walked with the browser's back/forward buttons.
//
// Scope is the "big pages" only — `/`, `/video/<id>`, `/channel/<id>`,
// `/channels`, `/decide`, `/queue`, `/search`, `/add`, `/settings`. Sub-page state
// (library filters, in-transcript search text, the scrub position) stays in
// its own component and is intentionally NOT reflected in the URL.
//
// The backend already serves index.html for any unknown path (see
// backend/web/embed.go), so this is a pure-frontend concern — a cold-loaded
// deep link reaches the SPA, which then parses the path here.

import { useCallback, useEffect, useRef, useState } from "react";
import type { ViewId } from "./shell/Rail";

// RouteState is the parsed, in-memory form of the URL: which view is open,
// plus the id for the two id-bearing views (the Player's video, the Channel
// page's channel). It is the source of truth App renders from; the URL is
// derived from it via toPath, and it is derived from the URL via parsePath on
// load and on every back/forward.
export type RouteState = {
  view: ViewId;
  videoId: string | null;
  channelId: string | null;
};

const LIBRARY: RouteState = { view: "library", videoId: null, channelId: null };

// decodeSegment decodes a percent-encoded path segment, tolerating a malformed
// escape (a lone or invalid `%` from a hand-typed or mangled external URL) by
// returning the raw segment instead of throwing. parsePath runs synchronously
// in App's initial render (the useState initializer in useRoute) with no error
// boundary above it, so a bare `decodeURIComponent` throwing URIError here would
// white-screen the whole SPA. Falling back to the raw id keeps the view open and
// lets it resolve to a normal "not found" instead.
function decodeSegment(seg: string): string {
  try {
    return decodeURIComponent(seg);
  } catch {
    return seg;
  }
}

// parsePath maps a URL pathname to a RouteState. The first path segment picks
// the view; `video`/`channel` take the next segment as their id (a missing id
// is allowed — the view renders its own "nothing selected" state). Anything
// unrecognised falls back to the Library, mirroring the server-side SPA
// fallback. Segments are split with empty parts dropped, so a trailing or
// doubled slash parses the same as the canonical form.
export function parsePath(pathname: string): RouteState {
  const seg = pathname.split("/").filter(Boolean);
  switch (seg[0]) {
    case undefined:
      return LIBRARY;
    case "video":
      return {
        view: "player",
        videoId: seg[1] ? decodeSegment(seg[1]) : null,
        channelId: null,
      };
    case "channel":
      return {
        view: "channel",
        videoId: null,
        channelId: seg[1] ? decodeSegment(seg[1]) : null,
      };
    case "channels":
      return { view: "channels", videoId: null, channelId: null };
    case "search":
      return { view: "search", videoId: null, channelId: null };
    case "add":
      return { view: "add", videoId: null, channelId: null };
    case "decide":
      return { view: "decide", videoId: null, channelId: null };
    // /pending is the page's old path (it was "Pending" before the
    // Decide/Queue/Activity split). Keep parsing it to the same view so an open
    // tab or a saved bookmark doesn't 404 — useRoute's mount normalize then
    // rewrites the address bar to the canonical /decide.
    case "pending":
      return { view: "decide", videoId: null, channelId: null };
    case "queue":
      return { view: "queue", videoId: null, channelId: null };
    case "activity":
      return { view: "activity", videoId: null, channelId: null };
    case "settings":
      return { view: "settings", videoId: null, channelId: null };
    default:
      return LIBRARY;
  }
}

// toPath is the inverse of parsePath: the canonical URL for a RouteState.
// Only the id that belongs to the active view is encoded — navigating to the
// Library while a video is still selected in memory yields "/", never
// "/video/<id>" — so the URL never carries state the page isn't showing.
export function toPath(state: RouteState): string {
  switch (state.view) {
    case "library":
      return "/";
    case "player":
      return state.videoId
        ? `/video/${encodeURIComponent(state.videoId)}`
        : "/video";
    case "channel":
      return state.channelId
        ? `/channel/${encodeURIComponent(state.channelId)}`
        : "/channel";
    case "channels":
      return "/channels";
    case "search":
      return "/search";
    case "add":
      return "/add";
    case "decide":
      return "/decide";
    case "queue":
      return "/queue";
    case "activity":
      return "/activity";
    case "settings":
      return "/settings";
  }
}

// useRoute owns the RouteState and keeps it in lockstep with the browser URL.
// It seeds from the current pathname, re-derives on back/forward (popstate),
// and exposes navigate() for in-app transitions.
//
// navigate merges its patch onto the *current* route, so switching views keeps
// the selected video/channel in memory: after opening a video then clicking
// "Channels", the rail's "Now playing" still returns to that video (the URL is
// "/channels" but videoId survives in state). It pushState's the new URL only
// when the canonical path actually changes, so repeat clicks on the active
// view don't stack duplicate history entries.
//
// The merge reads a ref rather than closing over `route`, which keeps navigate
// referentially stable (safe to pass straight to children / effect deps) and
// correct even across several transitions within one tick. pushState is done
// from this event-driven callback — never inside a render or a state updater —
// so React StrictMode's double-invocation can't double-push.
export function useRoute(): {
  route: RouteState;
  navigate: (patch: Partial<RouteState>) => void;
} {
  const [route, setRoute] = useState<RouteState>(() =>
    parsePath(window.location.pathname),
  );
  const routeRef = useRef(route);
  routeRef.current = route;

  useEffect(() => {
    function onPop() {
      setRoute(parsePath(window.location.pathname));
    }
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  // Normalize a non-canonical entry URL once on mount (an unknown path, a
  // trailing slash) so the address bar matches what is actually shown. Uses
  // replaceState — this is a correction of the current entry, not a navigation
  // the user should be able to "go back" out of.
  useEffect(() => {
    const canonical = toPath(routeRef.current);
    if (canonical !== window.location.pathname) {
      window.history.replaceState(null, "", canonical);
    }
  }, []);

  const navigate = useCallback((patch: Partial<RouteState>) => {
    const next = { ...routeRef.current, ...patch };
    const path = toPath(next);
    if (path !== window.location.pathname) {
      window.history.pushState(null, "", path);
    }
    routeRef.current = next;
    setRoute(next);
  }, []);

  return { route, navigate };
}
