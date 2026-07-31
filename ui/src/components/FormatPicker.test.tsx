import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { FormatPicker } from "./FormatPicker";
import { PRESETS_NO_CUSTOM } from "../formatPresets";

// The open/close/Escape/outside-click behaviour is CategoryPicker's and is
// covered by CategoryPicker.test.tsx; a couple of cases are repeated here
// because FormatPicker owns its own copy of that code. What is genuinely new
// is the DEFAULT badge and the legacy raw-selector value.
describe("FormatPicker", () => {
  function open(name = /Format override/) {
    fireEvent.click(screen.getByRole("button", { name }));
  }

  it("offers the presets and the global row, but never Custom", () => {
    render(
      <FormatPicker value="" globalPreset="apple-1080p" onPick={vi.fn()} />,
    );
    open();

    const items = screen.getAllByRole("menuitemradio");
    // Every preset except "custom", plus the "use the global setting" row.
    expect(items).toHaveLength(PRESETS_NO_CUSTOM.length + 1);
    expect(items.some((i) => /custom/i.test(i.textContent ?? ""))).toBe(false);
    expect(items[0].getAttribute("aria-checked")).toBe("true");
  });

  it("badges the preset the global setting resolves to", () => {
    render(
      <FormatPicker value="" globalPreset="apple-vp9-4k" onPick={vi.fn()} />,
    );
    open();

    const badges = screen
      .getAllByRole("menuitemradio")
      .filter((i) => i.querySelector(".fmtmenu-tag"));
    expect(badges).toHaveLength(1);
    expect(badges[0].textContent).toContain("Apple VP9 4K");
  });

  // The global preset can be "custom", which has no row here. Badging nothing
  // is right; badging some other row would be a lie about what gets downloaded.
  it("badges nothing when the global preset is custom or unknown", () => {
    render(<FormatPicker value="" globalPreset="custom" onPick={vi.fn()} />);
    open();

    expect(document.querySelectorAll(".fmtmenu-tag")).toHaveLength(0);
  });

  it("checks the stored preset and reports a new pick", () => {
    const onPick = vi.fn();
    render(
      <FormatPicker
        value="apple-1080p"
        globalPreset="best-mp4"
        onPick={onPick}
      />,
    );

    open(/Apple AirPlay 1080p/);
    expect(
      screen
        .getByRole("menuitemradio", { name: /Apple AirPlay 1080p/ })
        .getAttribute("aria-checked"),
    ).toBe("true");

    fireEvent.click(
      screen.getByRole("menuitemradio", { name: /Apple VP9 4K/ }),
    );
    expect(onPick).toHaveBeenCalledWith("apple-vp9-4k");
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("does not write when the current value is re-picked", () => {
    const onPick = vi.fn();
    render(
      <FormatPicker value="best-mp4" globalPreset="best-mp4" onPick={onPick} />,
    );

    open(/Best available MP4/);
    fireEvent.click(
      screen.getByRole("menuitemradio", { name: /Best available MP4/ }),
    );

    expect(onPick).not.toHaveBeenCalled();
  });

  // A channel configured before this picker existed holds a hand-typed yt-dlp
  // selector. It must survive being looked at: showing it is what stops the
  // override being silently replaced with something that downloads differently.
  it("shows a legacy raw selector as-is and never picks on its own", () => {
    const onPick = vi.fn();
    render(
      <FormatPicker
        value="bestvideo[height<=1440]+bestaudio"
        globalPreset="apple-1080p"
        onPick={onPick}
      />,
    );

    const trigger = screen.getByRole("button", {
      name: /bestvideo\[height<=1440\]\+bestaudio/,
    });
    expect(trigger.className).toContain("legacy");
    expect(onPick).not.toHaveBeenCalled();

    // No row claims to be the current value, so any pick is a replacement.
    fireEvent.click(trigger);
    expect(
      screen
        .getAllByRole("menuitemradio")
        .some((i) => i.getAttribute("aria-checked") === "true"),
    ).toBe(false);
  });

  it("closes on Escape without picking anything", () => {
    const onPick = vi.fn();
    render(
      <FormatPicker value="" globalPreset="apple-1080p" onPick={onPick} />,
    );

    open();
    expect(screen.getByRole("menu")).toBeTruthy();

    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("menu")).toBeNull();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("closes on an outside click", () => {
    render(
      <div>
        <FormatPicker value="" globalPreset="apple-1080p" onPick={vi.fn()} />
        <button type="button">elsewhere</button>
      </div>,
    );

    open();
    fireEvent.mouseDown(screen.getByRole("button", { name: "elsewhere" }));

    expect(screen.queryByRole("menu")).toBeNull();
  });
});
