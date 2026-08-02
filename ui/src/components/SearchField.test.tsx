import { useState } from "react";
import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { SearchField } from "./SearchField";

// The field is controlled, so it needs an owner to hold the text — the same
// shape every list page gives it.
function Harness() {
  const [value, setValue] = useState("");
  return (
    <SearchField
      value={value}
      onChange={setValue}
      placeholder="Search"
      label="Search videos"
    />
  );
}

const box = () => screen.getByRole("searchbox", { name: "Search videos" });

describe("SearchField", () => {
  // The list filters on every keystroke, so Enter submits nothing. Taking it as
  // "done typing" and handing the field back is the only reading left; a caret
  // still blinking over filtered results says the box is waiting for more.
  it("gives up focus on Enter, keeping the text", () => {
    render(<Harness />);
    fireEvent.change(box(), { target: { value: "electrolytes" } });
    box().focus();
    expect(box()).toHaveFocus();

    fireEvent.keyDown(box(), { key: "Enter" });

    expect(box()).not.toHaveFocus();
    expect(box()).toHaveValue("electrolytes");
  });

  it("keeps focus while ordinary keys are typed", () => {
    render(<Harness />);
    box().focus();
    fireEvent.keyDown(box(), { key: "a" });
    expect(box()).toHaveFocus();
  });

  // "/" is the way back in, which is what makes giving the field up on Enter
  // reversible without reaching for the mouse.
  it("takes focus when / is pressed elsewhere on the page", () => {
    render(<Harness />);
    // Dispatched at the body, which is where a keystroke lands when nothing on
    // the page is focused — the listener itself is on window.
    fireEvent.keyDown(document.body, { key: "/" });
    expect(box()).toHaveFocus();
  });
});
