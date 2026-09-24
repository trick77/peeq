import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, waitFor } from "@testing-library/react";
import { App } from "./App";
import { streamDownloads, getMe, listDownloads } from "./api";
import type { SSEEvent } from "./api/stream";
import type { Job, User } from "./api/types";
import { resetProgressForTests } from "./shell/progressStore";

vi.mock("./api", async () => (await import("./test/apiMock")).apiMock());

// The Player is the heaviest thing in the shell and stays mounted for the
// whole session. This file swaps it for a render counter so the two hot
// paths — a download's progress ticks and an answer's tokens — can be shown
// not to reach it.
const playerRenders = vi.hoisted(() => ({ count: 0 }));
vi.mock("./views/Player", () => ({
  Player: () => {
    playerRenders.count += 1;
    return <div data-testid="player-stub" />;
  },
}));

let pushFrame: ((e: SSEEvent) => void) | null = null;

beforeEach(() => {
  resetProgressForTests();
  window.history.replaceState(null, "", "/video/v1");
  playerRenders.count = 0;
  pushFrame = null;
  vi.mocked(getMe).mockResolvedValue({ id: "u1", email: "a@b.c" } as User);
  vi.mocked(listDownloads).mockResolvedValue([
    {
      job_id: 3,
      video_id: "v3",
      title: "Downloading",
      channel_name: "",
      state: "running",
      priority: 0,
    } as Job,
  ]);
  vi.mocked(streamDownloads).mockImplementation((onEvent, _signal, onOpen) => {
    pushFrame = onEvent;
    onOpen?.();
    return new Promise<void>(() => {});
  });
});

describe("App render isolation", () => {
  it("does not re-render the Player on download progress ticks", async () => {
    render(<App />);
    await screen.findByTestId("player-stub", {}, { timeout: 8000 });
    await waitFor(() => expect(pushFrame).not.toBeNull());
    // Let the queue and status loads settle before counting.
    await waitFor(() => expect(listDownloads).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 50));
    const before = playerRenders.count;

    act(() => {
      for (let i = 1; i <= 5; i += 1) {
        pushFrame!({
          event: "progress",
          data: { job_id: 3, percent: i * 10, speed: "1MB/s", eta: "9s" },
        });
      }
    });
    await new Promise((r) => setTimeout(r, 50));
    expect(playerRenders.count).toBe(before);
  }, 15000);
});
