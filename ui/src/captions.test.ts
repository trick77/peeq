import { describe, it, expect, vi } from "vitest";
import { centerCues, centerCuesRef } from "./captions";

// jsdom implements neither TextTrack nor VTTCue, and the real classes are not
// constructible without a document that has loaded media. The fields are all
// centerCues reads, so plain objects stand in for them.
type FakeCue = {
  align: string;
  position: number | string;
  line: number | string;
};

function cue(): FakeCue {
  // What YouTube's auto-generated track actually carries.
  return { align: "start", position: 0, line: -1 };
}

function trackWith(...cues: object[]): TextTrack {
  return { cues } as unknown as TextTrack;
}

describe("centerCues", () => {
  it("resets YouTube's left-edge settings to the WebVTT defaults", () => {
    const a = cue();
    const b = cue();
    centerCues(trackWith(a, b));
    for (const c of [a, b]) {
      expect(c.align).toBe("center");
      expect(c.position).toBe("auto");
      expect(c.line).toBe("auto");
    }
  });

  it("does nothing to a track with no cues yet", () => {
    // track.cues is null until the browser has loaded the file — the state the
    // element is in for as long as the track's mode stays "disabled".
    expect(() => centerCues(trackWith())).not.toThrow();
    expect(() =>
      centerCues({ cues: null } as unknown as TextTrack),
    ).not.toThrow();
    expect(() => centerCues(null)).not.toThrow();
    expect(() => centerCues(undefined)).not.toThrow();
  });

  // A chapter or metadata cue is a bare TextTrackCue with none of the three
  // fields, so it is skipped rather than grown new ones.
  it("skips a cue that carries no placement", () => {
    const plain = { text: "x" };
    centerCues(trackWith(plain));
    expect(plain).toEqual({ text: "x" });
  });
});

describe("centerCuesRef", () => {
  function fakeTrackEl(readyState: number, track: TextTrack) {
    const listeners: Record<string, Array<() => void>> = {};
    return {
      readyState,
      track,
      addEventListener: vi.fn((type: string, fn: () => void) => {
        (listeners[type] ??= []).push(fn);
      }),
      removeEventListener: vi.fn((type: string, fn: () => void) => {
        listeners[type] = (listeners[type] ?? []).filter((f) => f !== fn);
      }),
      fire: (type: string) => (listeners[type] ?? []).forEach((f) => f()),
    };
  }

  it("centres the cues when the track finishes loading", () => {
    const c = cue();
    const el = fakeTrackEl(0, trackWith(c));
    centerCuesRef(el as unknown as HTMLTrackElement);
    // Nothing happens on mount — the cues do not exist yet.
    expect(c.align).toBe("start");
    el.fire("load");
    expect(c.align).toBe("center");
  });

  // readyState 2 is HTMLTrackElement.LOADED: the load event already fired, so
  // waiting for another one would wait forever.
  it("centres immediately when the track loaded before the ref ran", () => {
    const c = cue();
    const el = fakeTrackEl(2, trackWith(c));
    centerCuesRef(el as unknown as HTMLTrackElement);
    expect(c.align).toBe("center");
  });

  it("drops the listener when React detaches the element", () => {
    const el = fakeTrackEl(0, trackWith(cue()));
    const cleanup = centerCuesRef(el as unknown as HTMLTrackElement);
    cleanup?.();
    expect(el.removeEventListener).toHaveBeenCalledWith(
      "load",
      el.addEventListener.mock.calls[0][1],
    );
  });

  it("ignores the null React passes on unmount", () => {
    expect(centerCuesRef(null)).toBeUndefined();
  });
});
