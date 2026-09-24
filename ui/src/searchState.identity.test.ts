import { describe, it, expect, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { useSearchState } from "./searchState";

vi.mock("./api/search", () => ({ searchVideos: vi.fn() }));
vi.mock("./api/answer", () => ({ streamAnswer: vi.fn() }));

describe("useSearchState identity", () => {
  it("returns the same object across renders that change nothing", () => {
    const { result, rerender } = renderHook(() => useSearchState());
    const first = result.current;
    rerender();
    expect(result.current).toBe(first);
    expect(result.current.submit).toBe(first.submit);
  });
});
