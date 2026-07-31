import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { SleepTimer, SLEEP_PRESETS } from "./SleepTimer";

// The component had no test file of its own — it was covered only through
// Player.test.tsx, which exercises the countdown but never the pill itself.
describe("SleepTimer", () => {
  it("offers Off first, then every preset", () => {
    render(
      <SleepTimer
        remainingSeconds={null}
        armedMinutes={null}
        onArm={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Sleep timer off" }));

    const items = screen.getAllByRole("menuitemradio");
    expect(items).toHaveLength(SLEEP_PRESETS.length + 1);
    // Off leads: a timer you can't call off is worse than no timer.
    expect(items[0]).toHaveTextContent("Off");
    expect(items[0]).toHaveAttribute("aria-checked", "true");
  });

  it("arming reports the preset and closes", () => {
    const onArm = vi.fn();
    render(
      <SleepTimer remainingSeconds={null} armedMinutes={null} onArm={onArm} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Sleep timer off" }));
    fireEvent.click(screen.getByRole("menuitemradio", { name: "15 minutes" }));

    expect(onArm).toHaveBeenCalledWith(15);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("re-picking the running preset restarts it rather than being swallowed", () => {
    const onArm = vi.fn();
    render(
      <SleepTimer remainingSeconds={200} armedMinutes={15} onArm={onArm} />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: /Sleep timer: 15 minutes/ }),
    );
    fireEvent.click(screen.getByRole("menuitemradio", { name: "15 minutes" }));

    expect(onArm).toHaveBeenCalledWith(15);
  });

  it("hands its menu a placement for the flip-up CSS to key on", () => {
    render(
      <SleepTimer
        remainingSeconds={null}
        armedMinutes={null}
        onArm={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Sleep timer off" }));
    // jsdom measures nothing, so the value is always the "down" fallback.
    // What is under test is that the attribute is wired at all.
    expect(screen.getByRole("menu")).toHaveAttribute("data-place", "down");
  });
});
