import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { StorageSection } from "./StorageSection";
import type { ChannelStorage, StorageGroup } from "../../api/channels";

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

  it("follows a switch-off purge until kept stops falling", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const keptOf = (n: number) => ({
        on_disk: group(),
        kept: group({ videos: n }),
        other: group(),
      });
      vi.mocked(getChannelStorage).mockResolvedValueOnce(keptOf(8));
      const { rerender } = render(<StorageSection channelId="UCa" keepReads />);
      await screen.findByText(/8 videos without a file/);

      // Off: measured at once (purge barely started), then again while the
      // count falls, and no more once it reaches 0.
      vi.mocked(getChannelStorage)
        .mockResolvedValueOnce(keptOf(8))
        .mockResolvedValueOnce(keptOf(3))
        .mockResolvedValueOnce(keptOf(0));
      rerender(<StorageSection channelId="UCa" keepReads={false} />);
      await screen.findByText(/8 videos without a file/);
      await vi.advanceTimersByTimeAsync(3000);
      await screen.findByText(/3 videos without a file/);
      await vi.advanceTimersByTimeAsync(3000);
      await screen.findByText(/0 videos without a file/);
      await vi.advanceTimersByTimeAsync(9000);
      expect(getChannelStorage).toHaveBeenCalledTimes(4);
    } finally {
      vi.useRealTimers();
    }
  });

  it("measures once on a channel that is already off", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.mocked(getChannelStorage).mockResolvedValue({
        on_disk: group(),
        kept: group({ videos: 2 }),
        other: group(),
      });
      render(<StorageSection channelId="UCa" keepReads={false} />);
      await screen.findByText(/2 videos without a file/);
      await vi.advanceTimersByTimeAsync(9000);
      expect(getChannelStorage).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("drops a slow earlier answer that arrives after a newer one", async () => {
    let slow: (v: ChannelStorage) => void = () => {};
    vi.mocked(getChannelStorage).mockReturnValueOnce(
      new Promise<ChannelStorage>((r) => {
        slow = r;
      }),
    );
    const { rerender } = render(<StorageSection channelId="UCa" keepReads />);
    vi.mocked(getChannelStorage).mockResolvedValueOnce({
      on_disk: group(),
      kept: group({ videos: 1 }),
      other: group(),
    });
    rerender(<StorageSection channelId="UCa" keepReads={false} />);
    await screen.findByText(/1 video without a file/);
    slow({ on_disk: group(), kept: group({ videos: 9 }), other: group() });
    await Promise.resolve();
    expect(
      screen.queryByText(/9 videos without a file/),
    ).not.toBeInTheDocument();
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
