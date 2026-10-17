import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { StorageSection } from "./StorageSection";
import type { StorageGroup } from "../../api/channels";

vi.mock("../../api/channels", () => ({ getChannelStorage: vi.fn() }));

import { getChannelStorage } from "../../api/channels";

const MB = 1024 ** 2;

function group(over: Partial<StorageGroup> = {}): StorageGroup {
  return {
    videos: 0,
    file_bytes: 0,
    transcript_bytes: 0,
    summary_bytes: 0,
    search_text_bytes: 0,
    vector_bytes: 0,
    poster_bytes: 0,
    ...over,
  };
}

describe("StorageSection", () => {
  beforeEach(() => {
    vi.mocked(getChannelStorage).mockReset();
  });

  it("splits files, their data and what is kept", async () => {
    vi.mocked(getChannelStorage).mockResolvedValue({
      on_disk: group({
        videos: 4,
        file_bytes: 1.9 * 1024 ** 3,
        transcript_bytes: 1 * MB,
        search_text_bytes: 1 * MB,
        vector_bytes: 2 * MB,
      }),
      kept: group({
        videos: 8,
        transcript_bytes: 2 * MB,
        vector_bytes: 3 * MB,
      }),
      other: group(),
    });
    render(<StorageSection channelId="UCa" keepReads />);

    expect(await screen.findByText(/1\.9 GB/)).toBeInTheDocument();
    expect(screen.getByText(/4 videos on disk/)).toBeInTheDocument();
    expect(screen.getByText(/8 videos without a file/)).toBeInTheDocument();
    // Kept: 2 MB transcripts + 3 MB vectors.
    expect(screen.getByText(/Kept in Search/).parentElement).toHaveTextContent(
      "5 MB",
    );
    expect(screen.queryByText(/New and in progress/)).not.toBeInTheDocument();
  });

  it("shows the New tab and queued work only when there is some", async () => {
    vi.mocked(getChannelStorage).mockResolvedValue({
      on_disk: group(),
      kept: group(),
      other: group({ videos: 1, summary_bytes: 2048 }),
    });
    render(<StorageSection channelId="UCa" keepReads />);
    expect(await screen.findByText(/New and in progress/)).toBeInTheDocument();
    expect(screen.getByText(/1 video$/)).toBeInTheDocument();
  });

  it("says so when measuring fails", async () => {
    vi.mocked(getChannelStorage).mockRejectedValue(
      new Error("failed to measure channel storage"),
    );
    render(<StorageSection channelId="UCa" keepReads />);
    expect(
      await screen.findByText("failed to measure channel storage"),
    ).toBeInTheDocument();
  });

  it("measures again after a switch-off, once the purge had time", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.mocked(getChannelStorage).mockResolvedValue({
        on_disk: group(),
        kept: group({ videos: 8 }),
        other: group(),
      });
      const { rerender } = render(<StorageSection channelId="UCa" keepReads />);
      await screen.findByText(/8 videos without a file/);
      expect(getChannelStorage).toHaveBeenCalledTimes(1);

      vi.mocked(getChannelStorage).mockResolvedValue({
        on_disk: group(),
        kept: group(),
        other: group(),
      });
      rerender(<StorageSection channelId="UCa" keepReads={false} />);
      expect(getChannelStorage).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(3000);
      expect(getChannelStorage).toHaveBeenCalledTimes(3);
      expect(
        await screen.findByText(/0 videos without a file/),
      ).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("never shows another channel's numbers while measuring", async () => {
    vi.mocked(getChannelStorage).mockResolvedValueOnce({
      on_disk: group({ videos: 4 }),
      kept: group(),
      other: group(),
    });
    const { rerender } = render(<StorageSection channelId="UCa" keepReads />);
    await screen.findByText(/4 videos on disk/);

    vi.mocked(getChannelStorage).mockReturnValueOnce(new Promise(() => {}));
    rerender(<StorageSection channelId="UCb" keepReads />);
    expect(screen.queryByText(/4 videos on disk/)).not.toBeInTheDocument();
    expect(screen.getByText("Measuring…")).toBeInTheDocument();
  });
});
