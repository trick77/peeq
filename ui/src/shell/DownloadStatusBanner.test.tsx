import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { DownloadStatusBanner } from "./DownloadStatusBanner";

const healthy = {
  paused: false,
  low_disk: false,
  youtube_paused: false,
  youtube_pause_reason: "",
};

describe("DownloadStatusBanner", () => {
  it("renders nothing while the queue is healthy", () => {
    const { container } = render(
      <DownloadStatusBanner
        status={healthy}
        onFixCookie={() => {}}
        onResume={async () => {}}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("offers Resume for the kill-switch and awaits it", async () => {
    let release: () => void = () => {};
    const onResume = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    render(
      <DownloadStatusBanner
        status={{ ...healthy, youtube_paused: true }}
        onFixCookie={() => {}}
        onResume={onResume}
      />,
    );
    const resume = screen.getByRole("button", { name: /resume/i });
    fireEvent.click(resume);
    expect(onResume).toHaveBeenCalledTimes(1);
    expect(resume).toBeDisabled();
    release();
    await waitFor(() => expect(resume).not.toBeDisabled());
  });

  it("says so when resuming fails, instead of dropping the rejection", async () => {
    const onResume = vi.fn().mockRejectedValue(new Error("nope"));
    render(
      <DownloadStatusBanner
        status={{ ...healthy, youtube_paused: true }}
        onFixCookie={() => {}}
        onResume={onResume}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /resume/i }));
    expect(await screen.findByText(/couldn’t resume/i)).toBeInTheDocument();
  });

  it("drops a failed-resume message once the pause reason moves on", async () => {
    const onResume = vi.fn().mockRejectedValue(new Error("nope"));
    const { rerender } = render(
      <DownloadStatusBanner
        status={{ ...healthy, youtube_paused: true }}
        onFixCookie={() => {}}
        onResume={onResume}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /resume/i }));
    await screen.findByText(/couldn’t resume/i);
    rerender(
      <DownloadStatusBanner
        status={{
          ...healthy,
          youtube_paused: true,
          youtube_pause_reason: "3 downloads failed in a row",
        }}
        onFixCookie={() => {}}
        onResume={onResume}
      />,
    );
    expect(screen.queryByText(/couldn’t resume/i)).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent(/failed in a row/);
  });

  it("low disk outranks the cookie pause, and the cookie pause links to Settings", () => {
    const onFixCookie = vi.fn();
    const { rerender } = render(
      <DownloadStatusBanner
        status={{ ...healthy, paused: true, low_disk: true }}
        onFixCookie={onFixCookie}
        onResume={async () => {}}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(/low disk/i);
    rerender(
      <DownloadStatusBanner
        status={{ ...healthy, paused: true }}
        onFixCookie={onFixCookie}
        onResume={async () => {}}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    expect(onFixCookie).toHaveBeenCalled();
  });
});
