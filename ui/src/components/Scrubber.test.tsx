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

    // A band is placed as a percentage of the duration, so with no duration
    // there is no scale to place one on. Zero is what the bar gets before the
    // media reports its length, and for a row whose duration peeq never
    // learned (videos.duration_seconds is nullable, and the DTO omits it).
    it("draws no bands before the duration is known", () => {
      const { container } = render(
        <Scrubber
          currentSeconds={0}
          durationSeconds={0}
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
