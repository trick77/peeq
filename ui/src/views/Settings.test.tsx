import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
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
  ytdlp_version: "2026.01.01",
};

vi.mock("../api/settings", () => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  putCookie: vi.fn(),
}));
vi.mock("../api/ytdlp", () => ({
  getYtdlpVersion: vi.fn().mockResolvedValue("2026.01.01"),
  updateYtdlp: vi.fn(),
}));

import { getSettings, putCookie } from "../api/settings";

describe("Settings", () => {
  beforeEach(() => {
    vi.mocked(getSettings).mockReset();
    vi.mocked(putCookie).mockReset();
    vi.mocked(getSettings).mockResolvedValue(baseSettings);
    vi.mocked(putCookie).mockResolvedValue({ ...baseSettings, cookie_status: "valid" });
  });

  it("shows the cookie status from GET, never the cookie body", async () => {
    render(<Settings />);
    expect(await screen.findByText(/Active/)).toBeInTheDocument();
    // Nothing resembling a pasted cookie value should ever render.
    expect(screen.queryByText(/SID/)).toBeNull();
    const textarea = screen.getByLabelText("YouTube cookie") as HTMLTextAreaElement;
    expect(textarea.value).toBe("");
  });

  it("saving the cookie textarea posts to putCookie", async () => {
    const user = userEvent.setup();
    render(<Settings />);
    const textarea = await screen.findByLabelText("YouTube cookie");
    await user.type(textarea, ".youtube.com\tTRUE\t/\tTRUE\t123\tSID\tabc");
    await user.click(screen.getByRole("button", { name: /Save cookie/i }));

    await waitFor(() => {
      expect(putCookie).toHaveBeenCalledWith(".youtube.com\tTRUE\t/\tTRUE\t123\tSID\tabc");
    });
  });
});
