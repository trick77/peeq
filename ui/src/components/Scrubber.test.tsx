import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { Scrubber } from "./Scrubber";

const SPONSOR = { category: "sponsor", start_time: 10, end_time: 25 };
const INTRO = { category: "intro", start_time: 30, end_time: 40 };

function bands(container: HTMLElement) {
  return Array.from(container.querySelectorAll(".scrub .sb"));
}

describe("Scrubber", () => {
  describe("SponsorBlock bands", () => {
    it("places a band at the segment's share of the duration", () => {
      const { container } = render(
        <Scrubber
          currentSeconds={0}
          durationSeconds={100}
          segments={[SPONSOR]}
          onSeek={vi.fn()}
        />,
      );
      const [band] = bands(container) as HTMLElement[];
      expect(band.style.left).toBe("10%");
      expect(band.style.width).toBe("15%");
    });

    // The regression this file exists for. A video whose duration peeq never
    // learned (videos.duration_seconds is NULL for imports, and the DTO omits
    // it) reaches the bar with 0, and used to lose every band while the legend
    // below it still named them — the player skipped a segment it had never
    // drawn. The bar now gets its length from the media element instead, so
    // the only thing left to hold here is the shape of the guard.
    it("draws no bands, and no legend either, before the duration is known", () => {
      const { container, queryByText } = render(
        <Scrubber
          currentSeconds={0}
          durationSeconds={0}
          segments={[SPONSOR]}
          onSeek={vi.fn()}
        />,
      );
      expect(bands(container)).toHaveLength(0);
      expect(queryByText("skipped")).toBeNull();
    });

    // A media element reports Infinity for a stream of unknown length. Left
    // unguarded that is > 0, so every band came out at left: 0%, width: 0% —
    // in the DOM, and nowhere on the bar.
    it("draws no bands for an unbounded duration", () => {
      const { container } = render(
        <Scrubber
          currentSeconds={0}
          durationSeconds={Infinity}
          segments={[SPONSOR]}
          onSeek={vi.fn()}
        />,
      );
      expect(bands(container)).toHaveLength(0);
    });

    // Auto-skip categories and marked-only ones are drawn differently, and
    // the player reads the same set to decide what to jump.
    it("stripes an auto-skipped segment and a marked one apart", () => {
      const { container } = render(
        <Scrubber
          currentSeconds={0}
          durationSeconds={100}
          segments={[SPONSOR, INTRO]}
          onSeek={vi.fn()}
        />,
      );
      const [sponsor, intro] = bands(container);
      expect(sponsor).not.toHaveClass("mark");
      expect(sponsor).toHaveAttribute("title", "Skipped automatically: ad");
      expect(intro).toHaveClass("mark");
      expect(intro).toHaveAttribute("title", "Marked: intro");
    });
  });

  describe("seeking", () => {
    it("reports the clicked fraction of the duration", () => {
      const onSeek = vi.fn();
      const { container } = render(
        <Scrubber
          currentSeconds={0}
          durationSeconds={200}
          segments={[]}
          onSeek={onSeek}
        />,
      );
      const scrub = container.querySelector(".scrub")!;
      vi.spyOn(scrub, "getBoundingClientRect").mockReturnValue({
        left: 0,
        width: 400,
      } as DOMRect);

      fireEvent.click(scrub, { clientX: 100 });

      expect(onSeek).toHaveBeenCalledWith(50);
    });

    it("ignores a click before the duration is known", () => {
      const onSeek = vi.fn();
      const { container } = render(
        <Scrubber
          currentSeconds={0}
          durationSeconds={0}
          segments={[]}
          onSeek={onSeek}
        />,
      );
      fireEvent.click(container.querySelector(".scrub")!, { clientX: 100 });
      expect(onSeek).not.toHaveBeenCalled();
    });
  });
});
