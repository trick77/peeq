import { useSyncExternalStore } from "react";
import type { DownloadProgress, DownloadProgressEvent } from "../api/types";

// progressStore — live download progress, outside React state.
//
// The SSE "progress" event fires several times a second while a download
// runs. Held as App state, every tick re-rendered the whole shell: the rail,
// the current view, and the hidden Player with its transcript. Only Up next's
// download rows actually draw the numbers, so each row subscribes to its own
// job here and nothing else re-renders on a tick.
//
// The snapshot is replaced whole on every write, which is what
// useSyncExternalStore needs to tell "changed" from "same".

const EMPTY: Record<number, DownloadProgress> = Object.freeze({});
let snapshot: Record<number, DownloadProgress> = EMPTY;
const listeners = new Set<() => void>();

function set(next: Record<number, DownloadProgress>) {
  snapshot = next;
  for (const fn of listeners) fn();
}

// publishProgress records one event. The job id is the key; the rest is what
// the dock and the lane draw.
export function publishProgress(evt: DownloadProgressEvent): void {
  set({
    ...snapshot,
    [evt.job_id]: { percent: evt.percent, speed: evt.speed, eta: evt.eta },
  });
}

// pruneProgress keeps only the jobs still listed, so the store stays bounded
// to the queue rather than growing for every job of the session. A prune that
// changes nothing keeps the same snapshot, so subscribers do not re-render.
export function pruneProgress(keep: Iterable<number>): void {
  const ids = new Set(keep);
  const next: Record<number, DownloadProgress> = {};
  let dropped = false;
  for (const key of Object.keys(snapshot)) {
    const id = Number(key);
    if (ids.has(id)) next[id] = snapshot[id];
    else dropped = true;
  }
  if (dropped) set(next);
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

function getSnapshot() {
  return snapshot;
}

function getServerSnapshot() {
  return EMPTY;
}

// useProgressByJobId subscribes a component to the whole live map.
export function useProgressByJobId(): Record<number, DownloadProgress> {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

// useJobProgress subscribes a component to one job's progress: a tick for
// another job leaves it alone, so a page of rows re-renders one row per tick.
// null means "not a running job", which reads as no progress.
export function useJobProgress(
  jobId: number | null,
): DownloadProgress | undefined {
  return useSyncExternalStore(
    subscribe,
    () => (jobId === null ? undefined : snapshot[jobId]),
    () => undefined,
  );
}

// resetProgressForTests empties the store like any other write, so a
// subscriber still mounted from an earlier test sees the empty map too.
export function resetProgressForTests(): void {
  set(EMPTY);
}
