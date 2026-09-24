import { describe, it, expect, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import {
  publishProgress,
  pruneProgress,
  useProgressByJobId,
  useJobProgress,
  resetProgressForTests,
} from "./progressStore";

beforeEach(() => {
  resetProgressForTests();
});

describe("progressStore", () => {
  it("publishes a tick under its job id and notifies subscribers", () => {
    const { result } = renderHook(() => useProgressByJobId());
    expect(result.current).toEqual({});
    act(() =>
      publishProgress({ job_id: 7, percent: 40, speed: "1MB/s", eta: "10s" }),
    );
    expect(result.current[7]).toEqual({
      percent: 40,
      speed: "1MB/s",
      eta: "10s",
    });
  });

  it("a later tick for the same job replaces the earlier one", () => {
    const { result } = renderHook(() => useProgressByJobId());
    act(() => {
      publishProgress({ job_id: 7, percent: 40, speed: "", eta: "" });
      publishProgress({ job_id: 7, percent: 41, speed: "", eta: "" });
    });
    expect(result.current[7]?.percent).toBe(41);
    expect(Object.keys(result.current)).toHaveLength(1);
  });

  it("prunes jobs that are no longer listed, and keeps the snapshot when nothing changes", () => {
    const { result } = renderHook(() => useProgressByJobId());
    act(() => {
      publishProgress({ job_id: 1, percent: 1, speed: "", eta: "" });
      publishProgress({ job_id: 2, percent: 2, speed: "", eta: "" });
    });
    const before = result.current;
    act(() => pruneProgress([1, 2]));
    expect(result.current).toBe(before);
    act(() => pruneProgress([2]));
    expect(result.current).toEqual({ 2: { percent: 2, speed: "", eta: "" } });
  });

  it("a per-job subscription follows its own job and ignores the others", () => {
    const { result } = renderHook(() => useJobProgress(7));
    expect(result.current).toBeUndefined();
    act(() => publishProgress({ job_id: 8, percent: 5, speed: "", eta: "" }));
    expect(result.current).toBeUndefined();
    act(() => publishProgress({ job_id: 7, percent: 50, speed: "", eta: "" }));
    expect(result.current?.percent).toBe(50);
    const { result: none } = renderHook(() => useJobProgress(null));
    expect(none.current).toBeUndefined();
  });
});
