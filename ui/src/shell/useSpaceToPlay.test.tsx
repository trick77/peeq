import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { useSpaceToPlay } from "./useSpaceToPlay";
import { park, resetVideoHostForTests, videoHostNode } from "../videoHost";

function Harness() {
  useSpaceToPlay();
  return null;
}

let video: HTMLVideoElement;
let play: ReturnType<typeof vi.fn>;
let pause: ReturnType<typeof vi.fn>;

// A <video> in the host, parked where the test says. jsdom implements neither
// play() nor pause(), so both are stubs that flip `paused` the way a browser
// would.
function mountVideo(where: "stage" | "dock" | null, paused = true) {
  video = document.createElement("video");
  let isPaused = paused;
  Object.defineProperty(video, "paused", { get: () => isPaused });
  play = vi.fn(() => {
    isPaused = false;
    return Promise.resolve();
  });
  pause = vi.fn(() => {
    isPaused = true;
  });
  video.play = play as unknown as HTMLVideoElement["play"];
  video.pause = pause as unknown as HTMLVideoElement["pause"];
  videoHostNode().appendChild(video);
  if (where) {
    const slot = document.createElement("div");
    document.body.appendChild(slot);
    park(where, slot);
  }
}

function space(
  target: EventTarget = document.body,
  init: KeyboardEventInit = {},
) {
  const e = new KeyboardEvent("keydown", {
    key: " ",
    code: "Space",
    bubbles: true,
    cancelable: true,
    ...init,
  });
  target.dispatchEvent(e);
  return e;
}

beforeEach(() => {
  render(<Harness />);
});

afterEach(() => {
  park("stage", null);
  park("dock", null);
  resetVideoHostForTests();
  document.body.innerHTML = "";
});

describe("useSpaceToPlay", () => {
  it("plays and pauses the video on the player page", () => {
    mountVideo("stage");
    const e = space();
    expect(play).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
    space();
    expect(pause).toHaveBeenCalledTimes(1);
  });

  it("drives the video in the dock too", () => {
    mountVideo("dock", false);
    space();
    expect(pause).toHaveBeenCalledTimes(1);
  });

  // Stopped from the dock: the element may linger in limbo, and Space must not
  // start a video nobody can see.
  it("leaves Space alone when no video is on screen", () => {
    mountVideo(null);
    const e = space();
    expect(play).not.toHaveBeenCalled();
    expect(e.defaultPrevented).toBe(false);
  });

  it("leaves Space alone with nothing loaded", () => {
    const e = space();
    expect(e.defaultPrevented).toBe(false);
  });

  it.each([
    ["a text field", () => document.createElement("input")],
    ["a textarea", () => document.createElement("textarea")],
    ["a select", () => document.createElement("select")],
    ["a button", () => document.createElement("button")],
    [
      "a link",
      () => {
        const a = document.createElement("a");
        a.href = "#";
        return a;
      },
    ],
    [
      "editable text",
      () => {
        const d = document.createElement("div");
        d.setAttribute("contenteditable", "true");
        return d;
      },
    ],
  ])("leaves Space to %s", (_name, make) => {
    mountVideo("stage");
    const el = make();
    document.body.appendChild(el);
    const e = space(el);
    expect(play).not.toHaveBeenCalled();
    expect(e.defaultPrevented).toBe(false);
  });

  // Focused, the element's native controls already answer Space; taking it as
  // well would toggle twice, which looks like nothing happened.
  it("leaves Space to the video element itself", () => {
    mountVideo("stage");
    space(video);
    expect(play).not.toHaveBeenCalled();
  });

  it("ignores Space with a modifier, and key repeat", () => {
    mountVideo("stage");
    space(document.body, { shiftKey: true });
    space(document.body, { metaKey: true });
    space(document.body, { repeat: true });
    expect(play).not.toHaveBeenCalled();
  });
});
