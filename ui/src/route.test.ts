import { describe, it, expect } from "vitest";
import { parsePath, toPath, type RouteState } from "./route";

describe("parsePath", () => {
  it("maps / to the Library", () => {
    expect(parsePath("/")).toEqual({
      view: "library",
      videoId: null,
      channelId: null,
    });
  });

  it("maps /video/<id> to the Player with that video", () => {
    expect(parsePath("/video/dQw4w9WgXcQ")).toEqual({
      view: "player",
      videoId: "dQw4w9WgXcQ",
      channelId: null,
    });
  });

  it("maps /video with no id to the Player with a null video", () => {
    expect(parsePath("/video")).toEqual({
      view: "player",
      videoId: null,
      channelId: null,
    });
  });

  it("maps /channel/<id> to the Channel page with that channel", () => {
    expect(parsePath("/channel/UCabc123")).toEqual({
      view: "channel",
      videoId: null,
      channelId: "UCabc123",
    });
  });

  it("distinguishes /channels (the list) from /channel/<id> (a detail page)", () => {
    expect(parsePath("/channels").view).toBe("channels");
    expect(parsePath("/channel/UCabc123").view).toBe("channel");
  });

  it("maps the plain views", () => {
    expect(parsePath("/search").view).toBe("search");
    expect(parsePath("/add").view).toBe("add");
    expect(parsePath("/decide").view).toBe("decide");
    expect(parsePath("/queue").view).toBe("queue");
    expect(parsePath("/settings").view).toBe("settings");
  });

  it("keeps the old /pending path pointing at Decide (soft redirect)", () => {
    // /pending was the page's path before the Decide/Queue split. It still
    // parses to the decide view so a bookmark or open tab doesn't 404;
    // useRoute's mount normalize then rewrites the bar to /decide.
    expect(parsePath("/pending").view).toBe("decide");
  });

  it("falls back to the Library for an unknown path", () => {
    expect(parsePath("/bogus").view).toBe("library");
    expect(parsePath("/video/../etc").view).toBe("player");
  });

  it("ignores a trailing slash and doubled slashes", () => {
    expect(parsePath("/settings/")).toEqual(parsePath("/settings"));
    expect(parsePath("//decide//")).toEqual(parsePath("/decide"));
    expect(parsePath("/channel/UCabc123/")).toEqual(
      parsePath("/channel/UCabc123"),
    );
  });

  it("decodes a percent-encoded id segment", () => {
    expect(parsePath("/channel/UC%2Fweird").channelId).toBe("UC/weird");
  });

  it("tolerates a malformed percent-escape instead of throwing", () => {
    // A hand-typed or mangled external URL can carry a lone/invalid `%`
    // (e.g. `/video/100%`) or an invalid UTF-8 escape (`/video/%FF`).
    // parsePath runs in App's first render with no error boundary, so it must
    // never throw — it keeps the view and falls back to the raw id.
    expect(() => parsePath("/video/100%")).not.toThrow();
    expect(parsePath("/video/100%")).toEqual({
      view: "player",
      videoId: "100%",
      channelId: null,
    });
    expect(() => parsePath("/channel/%FF")).not.toThrow();
    expect(parsePath("/channel/%FF").channelId).toBe("%FF");
  });

  it("ignores extra path segments after the id", () => {
    expect(parsePath("/video/v1/extra")).toEqual({
      view: "player",
      videoId: "v1",
      channelId: null,
    });
  });
});

describe("toPath", () => {
  const cases: [RouteState, string][] = [
    [{ view: "library", videoId: null, channelId: null }, "/"],
    [{ view: "player", videoId: "v1", channelId: null }, "/video/v1"],
    [{ view: "player", videoId: null, channelId: null }, "/video"],
    [{ view: "channel", videoId: null, channelId: "UCx" }, "/channel/UCx"],
    [{ view: "channel", videoId: null, channelId: null }, "/channel"],
    [{ view: "channels", videoId: null, channelId: null }, "/channels"],
    [{ view: "search", videoId: null, channelId: null }, "/search"],
    [{ view: "add", videoId: null, channelId: null }, "/add"],
    [{ view: "decide", videoId: null, channelId: null }, "/decide"],
    [{ view: "queue", videoId: null, channelId: null }, "/queue"],
    [{ view: "settings", videoId: null, channelId: null }, "/settings"],
  ];

  it.each(cases)("builds %o -> %s", (state, path) => {
    expect(toPath(state)).toBe(path);
  });

  it("encodes only the id relevant to the active view", () => {
    // A video selected in memory but the active view is the Library: the URL
    // is "/", never "/video/<id>". The URL never carries state the page is
    // not showing.
    expect(toPath({ view: "library", videoId: "v1", channelId: "UCx" })).toBe(
      "/",
    );
  });
});

describe("round-trip", () => {
  // Every canonical path survives parse -> build unchanged. This is what makes
  // the mount normalization stable: a valid entry URL is never rewritten.
  const canonical = [
    "/",
    "/video/v1",
    "/video",
    "/channel/UCx",
    "/channel",
    "/channels",
    "/search",
    "/add",
    "/decide",
    "/queue",
    "/settings",
  ];

  it.each(canonical)("%s -> parse -> toPath is stable", (path) => {
    expect(toPath(parsePath(path))).toBe(path);
  });
});
