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
  direct_stream_enabled: false,
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
  getYtdlpVersion: vi.fn(),
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
import { getYtdlpVersion, updateYtdlp } from "../api/ytdlp";

describe("Settings", () => {
  beforeEach(() => {
    vi.mocked(getSettings).mockReset();
    vi.mocked(putCookie).mockReset();
    vi.mocked(updateSettings).mockReset();
    vi.mocked(pauseYoutube).mockReset();
    vi.mocked(resumeYoutube).mockReset();
    vi.mocked(getAPITokenStatus).mockReset();
    vi.mocked(updateYtdlp).mockReset();
    vi.mocked(getYtdlpVersion).mockReset();
    vi.mocked(getYtdlpVersion).mockResolvedValue({
      version: "2026.01.01",
      latest: "2026.01.01",
      update_available: false,
      checked_at: new Date().toISOString(),
    });
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

  // Each Playback toggle owns its explanation. Flat siblings looked fine until
  // a second toggle landed underneath the first one's note, at which point the
  // note sat the same distance from both and read as belonging to neither.
  it("keeps each playback toggle in one group with its own note", async () => {
    render(<Settings />);

    for (const [name, phrase] of [
      [/show subtitles by default/i, /player’s subtitles button/i],
      [/allow direct playback links/i, /links expire after 12 hours/i],
    ] as const) {
      const group = (await screen.findByRole("checkbox", { name })).closest(
        ".toggle-field",
      );
      expect(group).not.toBeNull();
      // The note must live inside its own toggle's group, not merely somewhere
      // on the page — that is the difference this test exists to catch.
      expect(group).toHaveTextContent(phrase);
    }
  });

  it("toggles direct playback links", async () => {
    const user = userEvent.setup();
    vi.mocked(updateSettings).mockResolvedValue({
      ...baseSettings,
      direct_stream_enabled: true,
    });
    render(<Settings />);
    const toggle = await screen.findByRole("checkbox", {
      name: /allow direct playback links/i,
    });
    // Off by default: an install that never touches this has no auth-free
    // media route at all.
    expect(toggle).not.toBeChecked();

    await user.click(toggle);

    await waitFor(() => {
      expect(updateSettings).toHaveBeenCalledWith({
        direct_stream_enabled: true,
      });
    });
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

  // The Update button used to report nothing at all on success. When the
  // installed build was already current the returned version matched the one on
  // screen, so a working update was indistinguishable from a dead button —
  // which is exactly how this was reported.
  it("says so when the yt-dlp update lands on the same version", async () => {
    vi.mocked(updateYtdlp).mockResolvedValue({
      version: "2026.01.01",
      previous_version: "2026.01.01",
      updated: false,
    });
    render(<Settings />);
    fireEvent.click(await screen.findByRole("button", { name: /^Update$/ }));

    expect(
      await screen.findByText("Already on the latest version."),
    ).toBeInTheDocument();
  });

  it("names both versions when the yt-dlp update moves the version", async () => {
    vi.mocked(updateYtdlp).mockResolvedValue({
      version: "2026.08.15",
      previous_version: "2026.01.01",
      updated: true,
    });
    render(<Settings />);
    fireEvent.click(await screen.findByRole("button", { name: /^Update$/ }));

    expect(
      await screen.findByText("Updated 2026.01.01 → 2026.08.15."),
    ).toBeInTheDocument();
  });

  it("reports the installed version and a successful release check", async () => {
    render(<Settings />);
    expect(await screen.findByText("2026.01.01")).toBeInTheDocument();
    expect(
      await screen.findByText(/Latest release 2026\.01\.01/),
    ).toBeInTheDocument();
  });

  it("names the newer release when one is waiting", async () => {
    vi.mocked(getYtdlpVersion).mockResolvedValue({
      version: "2026.01.01",
      latest: "2026.08.15",
      update_available: true,
      checked_at: new Date().toISOString(),
    });
    render(<Settings />);
    expect(
      await screen.findByText(/2026\.08\.15 is available/),
    ).toBeInTheDocument();
  });

  // The rail indicator can only appear when a check SUCCEEDED, so a check that
  // keeps failing would otherwise be indistinguishable from being up to date.
  // This line is the only place that failure is ever admitted.
  it("admits when the release check cannot reach GitHub", async () => {
    vi.mocked(getYtdlpVersion).mockResolvedValue({
      version: "2026.01.01",
      latest: "2026.08.15",
      update_available: true,
      checked_at: "2026-07-01T00:00:00Z",
      check_error: "dial tcp: lookup api.github.com: no such host",
    });
    render(<Settings />);

    expect(
      await screen.findByText(/Couldn't reach GitHub/),
    ).toBeInTheDocument();
    // The stale answer is still worth showing — it is the best available —
    // so long as its age is shown with it.
    expect(
      screen.getByText(/Last known release 2026\.08\.15/),
    ).toBeInTheDocument();
  });

  it("clears the pending-update note once the update is installed", async () => {
    vi.mocked(getYtdlpVersion).mockResolvedValue({
      version: "2026.01.01",
      latest: "2026.08.15",
      update_available: true,
      checked_at: new Date().toISOString(),
    });
    vi.mocked(updateYtdlp).mockResolvedValue({
      version: "2026.08.15",
      previous_version: "2026.01.01",
      updated: true,
    });
    render(<Settings />);
    await screen.findByText(/2026\.08\.15 is available/);

    fireEvent.click(await screen.findByRole("button", { name: /^Update$/ }));

    expect(
      await screen.findByText(/Latest release 2026\.08\.15/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/is available/)).toBeNull();
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
