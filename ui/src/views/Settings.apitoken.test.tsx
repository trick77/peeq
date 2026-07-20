import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Settings } from "./Settings";
import { getSettings, getAPITokenStatus, createAPIToken } from "../api/settings";

// Mirror Settings.test.tsx's mock of ../api/settings, extended with the two
// token functions.
vi.mock("../api/settings", () => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  putCookie: vi.fn(),
  cookieHealth: vi.fn(),
  getAPITokenStatus: vi.fn(),
  createAPIToken: vi.fn(),
}));

const baseSettings = {
  cookie_status: "valid",
  cookie_updated_at: "2026-07-18T10:00:00Z",
  format_preset: "apple-1080p",
  format_custom: "",
  limit_rate: "",
  throttle_base_seconds: 20,
  retention_days: 14,
  min_free_gb: 5,
  min_video_duration_seconds: 180,
  ytdlp_version: "2026.07.01",
  youtube_paused: false,
  youtube_pause_reason: "",
};

function tokenSection(): HTMLElement {
  return screen.getByRole("heading", { name: /API token/i }).closest("section") as HTMLElement;
}

describe("Settings — API token", () => {
  beforeEach(() => {
    vi.mocked(getSettings).mockResolvedValue(baseSettings as never);
    vi.mocked(getAPITokenStatus).mockReset();
    vi.mocked(createAPIToken).mockReset();
  });

  it("offers to generate a token when none exists", async () => {
    // Given
    vi.mocked(getAPITokenStatus).mockResolvedValue({ present: false });

    // When
    render(<Settings />);

    // Then
    const section = await waitFor(() => tokenSection());
    expect(within(section).getByRole("button", { name: /Generate token/i })).toBeInTheDocument();
  });

  it("shows the token once after generating it", async () => {
    // Given
    vi.mocked(getAPITokenStatus).mockResolvedValue({ present: false });
    vi.mocked(createAPIToken).mockResolvedValue({
      token: "peeq_7Kd2mQx9vRtY4nLpZbA6sWfE8hJcU3iO1gTaXkPqRmN",
      created_at: "2026-07-20T09:12:00Z",
    });
    const user = userEvent.setup();
    render(<Settings />);
    const section = await waitFor(() => tokenSection());

    // When
    await user.click(within(section).getByRole("button", { name: /Generate token/i }));

    // Then: the plaintext is rendered, with the copy-it-now warning.
    await waitFor(() => {
      expect(
        within(tokenSection()).getByText("peeq_7Kd2mQx9vRtY4nLpZbA6sWfE8hJcU3iO1gTaXkPqRmN"),
      ).toBeInTheDocument();
    });
    expect(within(tokenSection()).getByText(/won't be shown again/i)).toBeInTheDocument();
  });

  it("never renders a token value on a returning visit", async () => {
    // Given: a token exists but, being hashed, cannot be fetched.
    vi.mocked(getAPITokenStatus).mockResolvedValue({
      present: true,
      created_at: "2026-07-20T09:12:00Z",
    });

    // When
    render(<Settings />);

    // Then: only a regenerate affordance, no secret.
    const section = await waitFor(() => tokenSection());
    expect(within(section).getByRole("button", { name: /Generate a new token/i })).toBeInTheDocument();
    expect(section.textContent).not.toMatch(/peeq_/);
  });

  it("requires confirmation before replacing an existing token", async () => {
    // Given
    vi.mocked(getAPITokenStatus).mockResolvedValue({
      present: true,
      created_at: "2026-07-20T09:12:00Z",
    });
    vi.mocked(createAPIToken).mockResolvedValue({
      token: "peeq_NEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEW",
      created_at: "2026-07-20T10:00:00Z",
    });
    const user = userEvent.setup();
    render(<Settings />);
    const section = await waitFor(() => tokenSection());

    // When: the first click only opens the confirm row.
    await user.click(within(section).getByRole("button", { name: /Generate a new token/i }));

    // Then: nothing has been created yet.
    expect(createAPIToken).not.toHaveBeenCalled();
    expect(within(tokenSection()).getByText(/stop sending cookies/i)).toBeInTheDocument();

    // When: the confirm is clicked.
    await user.click(within(tokenSection()).getByRole("button", { name: /^Generate$/ }));

    // Then
    await waitFor(() => expect(createAPIToken).toHaveBeenCalledTimes(1));
  });
});
