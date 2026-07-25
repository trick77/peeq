import { describe, it, expect } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { ThumbFill } from "./ThumbFill";

// The poster is decorative (alt=""), so it has no accessible role to query —
// these reach for the img/div by class, the same way the CSS does.
function img(container: HTMLElement) {
  return container.querySelector("img.fill");
}
function placeholder(container: HTMLElement) {
  return container.querySelector("div.fill");
}

describe("ThumbFill", () => {
  it("renders the poster when the video has one", () => {
    const { container } = render(<ThumbFill id="v1" hasThumbnail />);
    expect(img(container)).toHaveAttribute("src", "/api/videos/v1/thumbnail");
    expect(placeholder(container)).toBeNull();
  });

  it("renders the gradient placeholder when the video has no thumbnail", () => {
    const { container } = render(<ThumbFill id="v1" hasThumbnail={false} />);
    expect(img(container)).toBeNull();
    expect(placeholder(container)).toBeInTheDocument();
  });

  // The database says there is a thumbnail, the file 404s. Without the
  // fallback the browser draws its own broken-image glyph — which is exactly
  // what tombstoned cards used to show.
  it("falls back to the gradient when the poster fails to load", () => {
    const { container } = render(<ThumbFill id="v1" hasThumbnail />);
    fireEvent.error(img(container)!);
    expect(img(container)).toBeNull();
    expect(placeholder(container)).toBeInTheDocument();
  });

  // These cards are recycled as lists re-render; a failure remembered from
  // the previous video would blank a perfectly good poster.
  it("retries for a different video after a failure", () => {
    const { container, rerender } = render(<ThumbFill id="v1" hasThumbnail />);
    fireEvent.error(img(container)!);
    expect(img(container)).toBeNull();

    rerender(<ThumbFill id="v2" hasThumbnail />);
    expect(img(container)).toHaveAttribute("src", "/api/videos/v2/thumbnail");
  });
});
