import { describe, it, expect, beforeEach } from "vitest";
import { readNowPlaying, writeNowPlaying, clearNowPlaying } from "./nowPlaying";

describe("nowPlaying", () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it("returns null when nothing is stored", () => {
    expect(readNowPlaying()).toBeNull();
  });

  it("round-trips a written marker", () => {
    writeNowPlaying("v1", true);
    expect(readNowPlaying()).toEqual({ videoId: "v1", playing: true });
    writeNowPlaying("v1", false);
    expect(readNowPlaying()).toEqual({ videoId: "v1", playing: false });
  });

  it("clears the marker", () => {
    writeNowPlaying("v1", true);
    clearNowPlaying();
    expect(readNowPlaying()).toBeNull();
  });

  it("returns null for a malformed marker", () => {
    sessionStorage.setItem("peeq.nowPlaying", "not json");
    expect(readNowPlaying()).toBeNull();
    sessionStorage.setItem("peeq.nowPlaying", JSON.stringify({ videoId: 1, playing: "yes" }));
    expect(readNowPlaying()).toBeNull();
  });
});
