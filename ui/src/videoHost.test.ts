import { afterEach, describe, expect, it } from "vitest";
import {
  hostedVideo,
  park,
  resetVideoHostForTests,
  videoHostNode,
} from "./videoHost";

afterEach(() => {
  resetVideoHostForTests();
  document.body.innerHTML = "";
});

describe("videoHost", () => {
  it("keeps the host in the document before either slot registers", () => {
    // The premise the whole mechanism rests on: a media element removed from
    // the document is paused by the UA, so the host must never be parentless.
    const host = videoHostNode();
    expect(document.body.contains(host)).toBe(true);
  });

  it("parks on the stage when the stage registers", () => {
    const stage = document.createElement("div");
    document.body.appendChild(stage);
    park("stage", stage);
    expect(videoHostNode().parentElement).toBe(stage);
  });

  it("prefers the stage while both slots are registered", () => {
    const stage = document.createElement("div");
    const dock = document.createElement("div");
    document.body.append(stage, dock);
    park("dock", dock);
    park("stage", stage);
    expect(videoHostNode().parentElement).toBe(stage);
  });

  it("hands the video to the dock when the stage lets go", () => {
    const stage = document.createElement("div");
    const dock = document.createElement("div");
    document.body.append(stage, dock);
    park("stage", stage);
    park("dock", dock);
    park("stage", null);
    expect(videoHostNode().parentElement).toBe(dock);
  });

  it("stays in the document through a handover with no slot registered", () => {
    // React runs the outgoing component's cleanup before the incoming
    // component's effect, so there is a moment where neither slot is
    // registered. Dropping the host out of the document there is what would
    // pause playback mid-navigation.
    const stage = document.createElement("div");
    document.body.appendChild(stage);
    park("stage", stage);
    park("stage", null);
    expect(document.body.contains(videoHostNode())).toBe(true);
  });

  it("finds the hosted video wherever it is parked", () => {
    const video = document.createElement("video");
    videoHostNode().appendChild(video);
    const dock = document.createElement("div");
    document.body.appendChild(dock);
    park("dock", dock);
    expect(hostedVideo()).toBe(video);
  });

  it("reports no video before one is rendered", () => {
    expect(hostedVideo()).toBe(null);
  });
});
