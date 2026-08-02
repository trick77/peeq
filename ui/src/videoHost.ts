import { useSyncExternalStore } from "react";

// videoHost owns the one DOM node the <video> element is rendered into, so
// that playback can outlive the page it started on.
//
// The problem it solves: the <video> lives in Player's JSX, and a <video>
// removed from the document is paused by the UA. Watching something and then
// opening the Inbox to decide on a new upload therefore killed playback — the
// one thing peeq is for, interrupted by the other thing peeq is for.
//
// The fix is not to move the element between two React trees. Passing a
// different container to createPortal makes React unmount the old subtree and
// mount a fresh one: a brand new <video>, playing from 0:00. Instead the
// container is created ONCE and never replaced — React only ever manages its
// children — and this module relocates that container with appendChild. React
// never sees a move at all.
//
// That relocation is safe by spec: removing a media element from a document
// queues a task to pause it, and that task aborts if the element is back in a
// document by the time it runs. A synchronous appendChild to a new parent
// always is. Verified in Safari (the target browser for AirPlay) across
// stage -> dock -> stage -> dock while playing, including a React re-render of
// the portal's children mid-move: same node, still playing, no pause event.

// Where the host is currently parked. "stage" is the player page's video
// stage, "dock" is the now-playing bar's thumbnail. The distinction is not
// bookkeeping: the element wears native controls on the stage and must not in
// a 100x56 tile, so React has to be able to read this.
export type ParkedAt = "stage" | "dock" | null;

// The two slots that can host the video, each registered by the component that
// renders it. Both are held rather than a single "current target" so the
// handover never depends on the order React happens to run one component's
// cleanup and another's effect in: whoever is registered when the dust settles
// wins, and the stage always outranks the dock.
const slots: Record<"stage" | "dock", HTMLElement | null> = {
  stage: null,
  dock: null,
};

const listeners = new Set<() => void>();
let parkedAt: ParkedAt = null;
let host: HTMLDivElement | null = null;
// Where the host waits when neither slot is registered — which happens for the
// span of a single commit whenever the stage hands over to the dock. It is
// attached to <body> and merely pushed off-screen rather than detached,
// because detaching is the one thing that actually stops playback: a host left
// parentless during the handover would be out of the document, and while the
// spec's pause task would abort here too, relying on that for a gap we can
// simply not create would be a footgun for the next person moving these calls
// around.
let limbo: HTMLDivElement | null = null;

function ensure(): HTMLDivElement {
  if (!host) {
    host = document.createElement("div");
    host.className = "videohost";
    limbo = document.createElement("div");
    limbo.className = "videohost-limbo";
    document.body.appendChild(limbo);
    limbo.appendChild(host);
  }
  return host;
}

// videoHostNode is the portal target. Created on first read so that importing
// this module never touches the DOM — jsdom in a test that renders nothing
// would otherwise be handed a stray <div> on <body>.
export function videoHostNode(): HTMLDivElement {
  return ensure();
}

function apply() {
  const el = ensure();
  const next: ParkedAt = slots.stage ? "stage" : slots.dock ? "dock" : null;
  const target = slots.stage ?? slots.dock ?? limbo!;
  if (el.parentElement !== target) target.appendChild(el);
  if (parkedAt !== next) {
    parkedAt = next;
    for (const fn of listeners) fn();
  }
}

// park registers (or clears) one of the two slots. Callers pass their own slot
// name and their own node, so a component only ever speaks for itself: the
// player page clearing its stage on unmount cannot accidentally strand a dock
// that is already mounted.
export function park(where: "stage" | "dock", node: HTMLElement | null) {
  slots[where] = node;
  apply();
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

function snapshot(): ParkedAt {
  return parkedAt;
}

export function useParkedAt(): ParkedAt {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

// The <video> itself, for the dock's transport buttons and its own progress
// readout. Reached through the host rather than passed down as a ref because
// the element belongs to whichever Player rendered it, and the dock must not
// have to be a descendant of that Player to drive it.
//
// Driving the element directly is deliberate and not a shortcut: every seek
// and pause the dock performs fires the same timeupdate the player page's
// handler is already listening to, so resume tracking, the sleep timer and
// SponsorBlock auto-skip all stay correct without the dock knowing any of them
// exist.
export function hostedVideo(): HTMLVideoElement | null {
  return host?.querySelector("video") ?? null;
}

// Test-only: drop the module's DOM so one test's host cannot leak into the
// next. Not called by app code — the host is meant to live as long as the tab.
export function resetVideoHostForTests() {
  limbo?.remove();
  host = null;
  limbo = null;
  parkedAt = null;
  slots.stage = null;
  slots.dock = null;
  listeners.clear();
}
