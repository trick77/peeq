import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Add } from "./Add";

vi.mock("../api/downloads", async () => {
  const actual =
    await vi.importActual<typeof import("../api/downloads")>(
      "../api/downloads",
    );
  return {
    ...actual,
    addDownload: vi.fn(),
  };
});
vi.mock("../api/channels", () => ({
  addChannel: vi.fn(),
}));

import { addDownload } from "../api/downloads";
import { addChannel } from "../api/channels";

describe("Add", () => {
  beforeEach(() => {
    vi.mocked(addDownload).mockReset();
    vi.mocked(addChannel).mockReset();
    vi.mocked(addDownload).mockResolvedValue({
      job_id: 1,
      video_id: "v1",
      title: "A Video",
      channel_name: "Some Channel",
      state: "pending",
      priority: 0,
      attempts: 0,
    });
    vi.mocked(addChannel).mockResolvedValue({
      id: "c1",
      name: "A Channel",
      subscribed: false,
    });
  });

  it("submitting a channel URL calls addChannel, not addDownload", async () => {
    const user = userEvent.setup();
    render(<Add onQueued={() => {}} />);
    const input = screen.getByLabelText("Video or channel URL");
    await user.type(input, "https://www.youtube.com/@x");
    await user.click(screen.getByRole("button"));

    await waitFor(() => {
      expect(addChannel).toHaveBeenCalledWith(
        "https://www.youtube.com/@x",
        false,
      );
    });
    expect(addDownload).not.toHaveBeenCalled();
    expect(await screen.findByText(/Tracked A Channel/)).toBeInTheDocument();
  });

  it("submitting a video URL still uses the download path", async () => {
    const user = userEvent.setup();
    render(<Add onQueued={() => {}} />);
    const input = screen.getByLabelText("Video or channel URL");
    await user.type(input, "https://www.youtube.com/watch?v=abc12345678");
    await user.click(screen.getByRole("button"));

    await waitFor(() => {
      expect(addDownload).toHaveBeenCalledWith(
        "https://www.youtube.com/watch?v=abc12345678",
      );
    });
    expect(addChannel).not.toHaveBeenCalled();
  });
});
