import { useState } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import {
  ShareControl,
  ShareChip,
  shareChipLabel,
  daysUntil,
} from "./ShareControl";
import type { ShareStatus } from "../api/share";

vi.mock("../api", () => ({
  createShare: vi.fn(),
  stopShare: vi.fn(),
}));

import { createShare, stopShare } from "../api";

// A UTC expiry `days` from now, in the backend's 'YYYY-MM-DD HH:MM:SS' shape.
function expiryIn(days: number): string {
  return new Date(Date.now() + days * 86_400_000)
    .toISOString()
    .slice(0, 19)
    .replace("T", " ");
}

beforeEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText: vi.fn().mockResolvedValue(undefined) },
    configurable: true,
  });
});

describe("daysUntil / shareChipLabel", () => {
  it("returns null for a never-expiring link and a whole-day count otherwise", () => {
    expect(daysUntil(undefined)).toBeNull();
    expect(daysUntil(expiryIn(6))).toBe(6);
  });

  it("labels the chip with the countdown", () => {
    expect(shareChipLabel({ shared: true })).toBe("Shared");
    expect(shareChipLabel({ shared: true, expires_at: expiryIn(6) })).toBe(
      "Shared · 6 days left",
    );
  });
});

describe("ShareChip", () => {
  it("renders nothing when the video is not shared", () => {
    const { container } = render(<ShareChip status={{ shared: false }} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("goes amber in the final day", () => {
    const { container } = render(
      <ShareChip status={{ shared: true, expires_at: expiryIn(0.5) }} />,
    );
    expect(container.querySelector(".share-chip.soon")).not.toBeNull();
  });
});

// ShareControl renders no trigger of its own — the Player passes its ⋮ menu as
// children and owns the open flag. Harness plays that role: a trigger inside
// the wrapper, flipping a controlled boolean.
function Harness({
  status,
  onStatusChange = vi.fn(),
}: {
  status: ShareStatus;
  onStatusChange?: (s: ShareStatus) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <ShareControl
      videoId="v1"
      status={status}
      onStatusChange={onStatusChange}
      open={open}
      onOpenChange={setOpen}
    >
      <button type="button" onClick={() => setOpen((v) => !v)}>
        Video actions
      </button>
    </ShareControl>
  );
}

const openPopover = () =>
  fireEvent.click(screen.getByRole("button", { name: /video actions/i }));

describe("ShareControl", () => {
  it("creates a link from the unshared state with the default 7-day ttl", async () => {
    const created: ShareStatus = {
      shared: true,
      url: "https://peeq.example/s/tok123",
      token: "tok123",
      expires_at: expiryIn(7),
    };
    vi.mocked(createShare).mockResolvedValue(created);
    const onStatusChange = vi.fn();

    render(
      <Harness status={{ shared: false }} onStatusChange={onStatusChange} />,
    );

    openPopover();
    fireEvent.click(screen.getByRole("button", { name: /create link/i }));

    await waitFor(() => expect(createShare).toHaveBeenCalledWith("v1", "7d"));
    expect(onStatusChange).toHaveBeenCalledWith(created);
  });

  it("copies the link and confirms with a Copied state", async () => {
    const status: ShareStatus = {
      shared: true,
      url: "https://peeq.example/s/tok123",
      token: "tok123",
      expires_at: expiryIn(7),
    };
    render(<Harness status={status} />);

    openPopover();
    fireEvent.click(screen.getByRole("button", { name: /^copy$/i }));

    await waitFor(() =>
      expect(navigator.clipboard.writeText).toHaveBeenCalledWith(
        "https://peeq.example/s/tok123",
      ),
    );
    expect(await screen.findByText(/copied/i)).toBeInTheDocument();
  });

  it("re-stamps the expiry from a chip on an already-shared link", async () => {
    const status: ShareStatus = {
      shared: true,
      url: "https://x/s/t",
      token: "t",
      expires_at: expiryIn(7),
    };
    vi.mocked(createShare).mockResolvedValue({ ...status, expires_at: "" });
    render(<Harness status={status} />);

    openPopover();
    fireEvent.click(screen.getByRole("button", { name: /30 days/i }));
    await waitFor(() => expect(createShare).toHaveBeenCalledWith("v1", "30d"));
  });

  it("shows 'No expiry' in the popover for a never-expiring link", () => {
    render(
      <Harness status={{ shared: true, url: "https://x/s/t", token: "t" }} />,
    );
    openPopover();
    expect(screen.getByText(/no expiry/i)).toBeInTheDocument();
  });

  it("closes the popover on Escape", () => {
    render(<Harness status={{ shared: false }} />);
    openPopover();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("survives a mousedown on the trigger that opened it", () => {
    // The trigger lives inside the wrapper precisely so the click that opens
    // the popover doesn't read as an outside click and close it again.
    render(<Harness status={{ shared: false }} />);
    openPopover();
    fireEvent.mouseDown(screen.getByRole("button", { name: /video actions/i }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    // A mousedown anywhere else still closes it.
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("stops sharing only after a confirm step", async () => {
    vi.mocked(stopShare).mockResolvedValue({ shared: false });
    const onStatusChange = vi.fn();
    render(
      <Harness
        status={{ shared: true, url: "https://x/s/t", token: "t" }}
        onStatusChange={onStatusChange}
      />,
    );

    openPopover();
    // First click only arms the confirm — nothing sent yet.
    fireEvent.click(screen.getByRole("button", { name: /stop sharing/i }));
    expect(stopShare).not.toHaveBeenCalled();
    // The confirm actually stops.
    fireEvent.click(screen.getByRole("button", { name: /^stop$/i }));
    await waitFor(() => expect(stopShare).toHaveBeenCalledWith("v1"));
    expect(onStatusChange).toHaveBeenCalledWith({ shared: false });
  });
});
