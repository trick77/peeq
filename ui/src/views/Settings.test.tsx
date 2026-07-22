import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  render,
  screen,
  waitFor,
  fireEvent,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Settings } from "./Settings";
import type { Settings as SettingsType } from "../api/types";

const baseSettings: SettingsType = {
  cookie_status: "valid",
  cookie_updated_at: "2026-07-18T10:00:00Z",
  format_preset: "apple-1080p",
  format_custom: "",
  limit_rate: "4.5M",
  throttle_base_seconds: 20,
  retention_days: 14,
  min_free_gb: 5,
  min_video_duration_seconds: 60,
  subtitles_default: false,
  ytdlp_version: "2026.01.01",
  youtube_paused: false,
  youtube_pause_reason: "",
};

vi.mock("../api/settings", () => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  putCookie: vi.fn(),
  getAPITokenStatus: vi.fn(),
  createAPIToken: vi.fn(),
}));
vi.mock("../api/ytdlp", () => ({
  getYtdlpVersion: vi.fn().mockResolvedValue("2026.01.01"),
  updateYtdlp: vi.fn(),
}));
vi.mock("../api/downloads", () => ({
  pauseYoutube: vi.fn(),
  resumeYoutube: vi.fn(),
}));

import {
  getSettings,
  putCookie,
  updateSettings,
  getAPITokenStatus,
} from "../api/settings";
import { pauseYoutube, resumeYoutube } from "../api/downloads";

describe("Settings", () => {
  beforeEach(() => {
    vi.mocked(getSettings).mockReset();
    vi.mocked(putCookie).mockReset();
    vi.mocked(updateSettings).mockReset();
    vi.mocked(pauseYoutube).mockReset();
    vi.mocked(resumeYoutube).mockReset();
    vi.mocked(getAPITokenStatus).mockReset();
    vi.mocked(getSettings).mockResolvedValue(baseSettings);
    vi.mocked(putCookie).mockResolvedValue({
      ...baseSettings,
      cookie_status: "valid",
    });
    vi.mocked(updateSettings).mockResolvedValue(baseSettings);
    vi.mocked(pauseYoutube).mockResolvedValue(undefined);
    vi.mocked(resumeYoutube).mockResolvedValue(undefined);
    vi.mocked(getAPITokenStatus).mockResolvedValue({ present: false });
  });

  it("shows the cookie status from GET, never the cookie body", async () => {
    render(<Settings />);
    const cookieHeading = await screen.findByText("YouTube cookie");
    const cookieSection = cookieHeading.closest("section") as HTMLElement;
    expect(within(cookieSection).getByText(/Active/)).toBeInTheDocument();
    // Nothing resembling a pasted cookie value should ever render.
    expect(screen.queryByText(/SID/)).toBeNull();
    const textarea = screen.getByLabelText(
      "YouTube cookie",
    ) as HTMLTextAreaElement;
    expect(textarea.value).toBe("");
  });

  it("saving the cookie textarea posts to putCookie", async () => {
    const user = userEvent.setup();
    render(<Settings />);
    const textarea = await screen.findByLabelText("YouTube cookie");
    await user.type(textarea, ".youtube.com\tTRUE\t/\tTRUE\t123\tSID\tabc");
    await user.click(screen.getByRole("button", { name: /Save cookie/i }));

    await waitFor(() => {
      expect(putCookie).toHaveBeenCalledWith(
        ".youtube.com\tTRUE\t/\tTRUE\t123\tSID\tabc",
      );
    });
  });

  it("renders the current throttle_base_seconds value and saves it on blur", async () => {
    const user = userEvent.setup();
    render(<Settings />);
    const input = (await screen.findByLabelText(
      "Minimum delay between YouTube calls (seconds)",
    )) as HTMLInputElement;
    expect(input.value).toBe("20");

    await user.clear(input);
    await user.type(input, "45");
    await user.tab();

    await waitFor(() => {
      expect(updateSettings).toHaveBeenCalledWith({
        throttle_base_seconds: 45,
      });
    });
  });

  it("renders the current min_video_duration_seconds value and saves it on blur", async () => {
    const user = userEvent.setup();
    render(<Settings />);
    const input = (await screen.findByLabelText(
      "Ignore channel videos shorter than (seconds)",
    )) as HTMLInputElement;
    expect(input.value).toBe("60");

    await user.clear(input);
    await user.type(input, "90");
    await user.tab();

    await waitFor(() => {
      expect(updateSettings).toHaveBeenCalledWith({
        min_video_duration_seconds: 90,
      });
    });
  });

  it("toggles the global subtitles default", async () => {
    const user = userEvent.setup();
    vi.mocked(updateSettings).mockResolvedValue({
      ...baseSettings,
      subtitles_default: true,
    });
    render(<Settings />);
    const toggle = await screen.findByRole("checkbox", {
      name: /show subtitles by default/i,
    });
    expect(toggle).not.toBeChecked();

    await user.click(toggle);

    await waitFor(() => {
      expect(updateSettings).toHaveBeenCalledWith({ subtitles_default: true });
    });
    // The checkbox renders straight off the response, not off local state.
    await waitFor(() => expect(toggle).toBeChecked());
  });

  it("toggles the YouTube kill-switch", async () => {
    vi.mocked(getSettings).mockResolvedValue({
      ...baseSettings,
      youtube_paused: false,
      youtube_pause_reason: "",
    });
    render(<Settings />);
    const toggle = await screen.findByRole("checkbox", {
      name: /pause all youtube/i,
    });
    fireEvent.click(toggle);
    await waitFor(() => expect(pauseYoutube).toHaveBeenCalled());
  });

  it("shows the auto-pause reason when auto-engaged", async () => {
    vi.mocked(getSettings).mockResolvedValue({
      ...baseSettings,
      youtube_paused: true,
      youtube_pause_reason: "Auto-paused after repeated extractor failures.",
    });
    render(<Settings />);
    expect(
      await screen.findByText(/auto-paused after repeated/i),
    ).toBeInTheDocument();
  });
});
