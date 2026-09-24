import { useEffect, useSyncExternalStore } from "react";
import { getSettings } from "./api";
import type { Settings, SettingsPatch } from "./api/types";

// settingsStore — the one copy of the global settings the SPA reads.
//
// Five places used to fetch /api/settings on their own: the Settings page,
// the Library (retention), the Player (subtitles default, direct stream), and
// two channel tabs. Each read once per mount — and the Player never unmounts
// any more, so a preference changed on the Settings page did not reach it
// until a reload. Here every reader subscribes to the same value, the Settings
// page publishes what it saves, and a change lands everywhere at once.
//
// The value is a snapshot object replaced whole on every change, which is
// what useSyncExternalStore needs to tell "changed" from "same".

export type SettingsSnapshot = {
  // null until loaded, or after a failed load with nothing cached.
  settings: Settings | null;
  // The last load rejected and nothing is cached. Readers that must decide
  // (the Player's src) fall back to the pre-setting behaviour on this.
  failed: boolean;
};

const EMPTY: SettingsSnapshot = Object.freeze({
  settings: null,
  failed: false,
});

let snapshot: SettingsSnapshot = EMPTY;
let inflight: Promise<Settings> | null = null;
// Bumped by invalidate (and the test reset): a request that was out when the
// store was cleared must not repopulate it with the answer to a stale
// question when it finally lands.
let generation = 0;
const listeners = new Set<() => void>();

function set(next: SettingsSnapshot) {
  snapshot = next;
  for (const fn of listeners) fn();
}

function fetchInto(keepOnFailure: boolean): Promise<Settings> {
  if (inflight) return inflight;
  const gen = generation;
  inflight = getSettings().then(
    (s) => {
      if (gen !== generation) return s;
      inflight = null;
      set({ settings: s, failed: false });
      return s;
    },
    (err: unknown) => {
      if (gen !== generation) throw err;
      inflight = null;
      // A refresh that fails keeps what it had: a stale value beats a page
      // that forgets its retention window because one re-read timed out.
      if (!(keepOnFailure && snapshot.settings)) {
        set({ settings: null, failed: true });
      }
      throw err;
    },
  );
  return inflight;
}

// loadSettings resolves the cached value, or fetches it once for however
// many callers ask while the request is out. A failed load is not sticky:
// the next call fetches again.
export function loadSettings(): Promise<Settings> {
  if (snapshot.settings) return Promise.resolve(snapshot.settings);
  return fetchInto(false);
}

// refreshSettings always asks the server — the Settings page opening, where
// a cookie pasted from the extension must show — and publishes the answer.
export function refreshSettings(): Promise<Settings> {
  return fetchInto(true);
}

// publishSettings is what the Settings page calls with every save's response:
// the server's view is the truth, and every subscriber sees it.
export function publishSettings(s: Settings): void {
  set({ settings: s, failed: false });
}

// patchSettings applies an optimistic local change (the Player's CC toggle
// updates the subtitles default before the write lands).
export function patchSettings(patch: SettingsPatch): void {
  if (!snapshot.settings) return;
  set({ settings: { ...snapshot.settings, ...patch }, failed: false });
}

// invalidateSettings forgets everything: on sign-out, so a different user in
// the same tab never inherits the previous one's values.
export function invalidateSettings(): void {
  generation += 1;
  inflight = null;
  set(EMPTY);
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

function getSnapshot(): SettingsSnapshot {
  return snapshot;
}

function getServerSnapshot(): SettingsSnapshot {
  return EMPTY;
}

// useSettings subscribes a component to the shared value and starts a load
// if nothing is cached yet.
export function useSettings(): SettingsSnapshot {
  const snap = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
  useEffect(() => {
    if (!snapshot.settings) {
      loadSettings().catch(() => {});
    }
  }, []);
  return snap;
}

// resetSettingsStoreForTests drops the cached value and any request in
// flight, so one test's settings never leak into the next. Subscribers are
// left alone: they belong to whatever is still mounted.
export function resetSettingsStoreForTests(): void {
  generation += 1;
  inflight = null;
  snapshot = EMPTY;
}
