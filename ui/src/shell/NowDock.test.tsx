import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { NowDock } from "./NowDock";
import { park, resetVideoHostForTests, videoHostNode } from "../videoHost";
import type { NowPlaying } from "../nowPlaying";

const PLAYING: NowPlaying = {
  id: "vid1",
  title: "Bricklaying with a string line",
  channelName: "Essential Craftsman",
  durationSeconds: 600,
  segments: [
    { category: "sponsor", start_time: 60, end_time: 90 },
    // Not auto-skipped, so the bar must not mark it: a band drawn for a
    // segment that plays normally promises a jump that never comes.
    { category: "intro", start_time: 0, end_time: 10 },
  ],
};

// A <video> in the host, standing in for the one the Player portals in.
// jsdom implements neither play() nor pause(), so both are stubbed and the
// paused flag is driven by hand.
function hostVideo() {
  const el = document.createElement("video");
  let paused = true;
  Object.defineProperty(el, "paused", { get: () => paused });
  Object.defineProperty(el, "duration", { value: 600, configurable: true });
  el.play = vi.fn(() => {
    paused = false;
    el.dispatchEvent(new Event("play"));
    return Promise.resolve();
  });
  el.pause = vi.fn(() => {
    paused = true;
    el.dispatchEvent(new Event("pause"));
  });
  videoHostNode().appendChild(el);
  return el;
}

function renderDock(props: Partial<Parameters<typeof NowDock>[0]> = {}) {
  const onOpenPlayer = vi.fn();
  const onStop = vi.fn();
  const view = render(
    <NowDock
      playing={PLAYING}
      onOpenPlayer={onOpenPlayer}
      onStop={onStop}
      {...props}
    />,
  );
  return { onOpenPlayer, onStop, view };
}

afterEach(() => {
  resetVideoHostForTests();
});

describe("NowDock", () => {
  it("renders nothing when nothing is playing", () => {
    const { view } = renderDock({ playing: null });
    expect(view.container.querySelector(".nowdock")).toBeNull();
  });

  it("names what is playing", () => {
    renderDock();
    expect(screen.getByText(PLAYING.title)).toBeTruthy();
    expect(screen.getByText(/Essential Craftsman/)).toBeTruthy();
  });

  // The dock is hidden by WHERE THE VIDEO IS, not by the route. The summary
  // page shares /video/<id> with the player, and while reading one the dock
  // must still show — something really is still playing behind it.
  it("hides while the video is parked on the player's stage", () => {
    const stage = document.createElement("div");
    document.body.appendChild(stage);
    const { view } = renderDock();
    expect(view.container.querySelector(".nowdock")).toBeTruthy();
    act(() => park("stage", stage));
    expect(view.container.querySelector(".nowdock")).toBeNull();
    act(() => park("stage", null));
    expect(view.container.querySelector(".nowdock")).toBeTruthy();
    stage.remove();
  });

  it("parks the video in its tile", () => {
    hostVideo();
    renderDock();
    expect(videoHostNode().parentElement).toBe(
      document.querySelector(".nowdock-vid"),
    );
  });

  it("plays and pauses the hosted element", () => {
    const el = hostVideo();
    renderDock();
    fireEvent.click(screen.getByRole("button", { name: "Play" }));
    expect(el.play).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    expect(el.pause).toHaveBeenCalled();
  });

  // Asymmetric on purpose — see SKIP_BACK / SKIP_FORWARD.
  it("skips back 10 and forward 30", () => {
    const el = hostVideo();
    el.currentTime = 100;
    renderDock();
    fireEvent.click(screen.getByRole("button", { name: "Back 10 seconds" }));
    expect(el.currentTime).toBe(90);
    fireEvent.click(screen.getByRole("button", { name: "Forward 30 seconds" }));
    expect(el.currentTime).toBe(120);
  });

  it("never skips past either end", () => {
    const el = hostVideo();
    el.currentTime = 4;
    renderDock();
    fireEvent.click(screen.getByRole("button", { name: "Back 10 seconds" }));
    expect(el.currentTime).toBe(0);
    el.currentTime = 590;
    fireEvent.click(screen.getByRole("button", { name: "Forward 30 seconds" }));
    expect(el.currentTime).toBe(600);
  });

  it("marks only the segments that will actually be skipped", () => {
    const { view } = renderDock();
    const bands = view.container.querySelectorAll(".nowdock-progress s");
    expect(bands.length).toBe(1);
    // 60s into 600s, 30s long.
    expect((bands[0] as HTMLElement).style.left).toBe("10%");
    expect((bands[0] as HTMLElement).style.width).toBe("5%");
  });

  it("goes back to the player from the tile and the title", () => {
    const { onOpenPlayer } = renderDock();
    fireEvent.click(
      screen.getByRole("button", { name: `Back to ${PLAYING.title}` }),
    );
    fireEvent.click(screen.getByText(PLAYING.title));
    expect(onOpenPlayer).toHaveBeenCalledTimes(2);
  });

  it("stops on the close button", () => {
    const { onStop } = renderDock();
    fireEvent.click(screen.getByRole("button", { name: "Stop and close" }));
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("tracks the element's position", () => {
    const el = hostVideo();
    renderDock();
    act(() => {
      el.currentTime = 300;
      el.dispatchEvent(new Event("timeupdate"));
    });
    expect(screen.getByText("5:00 / 10:00")).toBeTruthy();
    expect(
      (document.querySelector(".nowdock-progress i") as HTMLElement).style
        .width,
    ).toBe("50%");
  });
});
