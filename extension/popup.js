import { DISPLAY_COOKIE_NAMES } from "./shared.js";

const api = globalThis.browser ?? globalThis.chrome;
const $ = (id) => document.getElementById(id);

// Captured from the initial status call and reused on every subsequent
// render so the host badge survives a send(), which only forwards
// {detail, count} and never carries baseUrl itself.
let knownBaseUrl = "";

// Amber, not red, for unreachable: it usually means you're on another network
// and nothing is broken. Red stays reserved for errors you must act on.
const VIEWS = {
  ready: { led: "ok", headline: "Ready to send", detail: "A YouTube sign-in is present in this profile." },
  sending: { led: "busy", headline: "Sending…", detail: "Handing the sign-in to peeq." },
  sent: { led: "ok", headline: "peeq has the sign-in", detail: "Accepted and validated. You can close this profile." },
  unreachable: { led: "warn", headline: "Can't reach peeq", detail: "Check that peeq is running and the address is right. Nothing was sent." },
  rejected: { led: "bad", headline: "peeq rejected the token", detail: "The access token is wrong, or was regenerated. Copy a fresh one from peeq's Settings." },
  "permission-denied": { led: "bad", headline: "Chrome permission missing", detail: "The extension needs permission to talk to peeq's address. Grant it in settings." },
  "server-error": { led: "bad", headline: "peeq refused the cookie", detail: "" },
  // Deliberately NOT a send state: nothing was sent, so it must not say peeq
  // refused anything. Raised when reading this profile's cookies fails.
  "status-error": { led: "bad", headline: "Couldn't read this profile's cookies", detail: "Chrome refused the cookie read. Try reopening the extension." },
  "no-session": { led: "idle", headline: "No YouTube sign-in in this profile", detail: "Sign in to YouTube in this Chrome profile, then come back." },
  "not-configured": { led: "idle", headline: "Connect to peeq", detail: "Add peeq's address and an access token to get started." },
  // Nothing was sent or even attempted here: the service worker never answered
  // (rejected sendMessage, or closed the channel with no response). Distinct
  // from "status-error" (a cookie read failed) and "server-error" (peeq
  // answered and refused). Reopening the extension usually restarts a
  // crashed/updating worker.
  "worker-error": { led: "bad", headline: "The extension didn't respond", detail: "peeq's background worker did not answer. Reopening the extension usually fixes it." },
  // The fallback for a state this popup doesn't recognise (e.g. one added
  // later on the status path). It must NOT borrow "server-error" or any
  // other view that asserts a specific cause — nothing here confirms
  // anything was sent, refused, or read, so the copy stays cause-neutral.
  unknown: { led: "bad", headline: "Something went wrong", detail: "peeq's extension hit an unexpected state. Reopening the extension usually fixes it." },
};

function render(state, extra = {}) {
  const view = VIEWS[state] ?? VIEWS.unknown;
  $("led").className = `led ${view.led === "idle" ? "" : view.led}`;
  $("headline").textContent = view.headline;
  $("detail").textContent = extra.detail || view.detail;
  if (extra.baseUrl !== undefined) knownBaseUrl = extra.baseUrl;
  $("host").textContent = knownBaseUrl;

  const facts = $("facts");
  facts.replaceChildren();
  // "Cookies sent" is only true after a successful send. Every other state
  // carrying a count is reporting what is PRESENT, so label it that way —
  // a no-session result must never read as though something was sent.
  const rows = [];
  if (state === "sent" && extra.count !== undefined) {
    rows.push(["Cookies sent", String(extra.count)]);
  } else if (extra.count !== undefined) {
    rows.push(["Sign-in cookies", `${extra.count} of ${extra.total ?? DISPLAY_COOKIE_NAMES.length} present`]);
  }
  facts.hidden = rows.length === 0;
  for (const [key, value] of rows) {
    const row = document.createElement("div");
    row.className = "fact";
    const dt = document.createElement("dt");
    dt.textContent = key;
    const dd = document.createElement("dd");
    dd.textContent = value;
    row.append(dt, dd);
    facts.append(row);
  }

  const actions = $("actions");
  actions.replaceChildren();
  // No send button without a session: an anonymous jar would overwrite
  // peeq's good cookie, so the action is removed rather than disabled.
  if (state === "ready" || state === "sent" || state === "unreachable" || state === "server-error") {
    actions.append(button(state === "sent" ? "Send again" : "Send cookie to peeq", "btn primary", send));
  }
  if (state !== "sending") {
    actions.append(button(state === "not-configured" ? "Get started" : "Settings", "btn ghost", () => api.runtime.openOptionsPage()));
  }
}

function button(label, className, onClick) {
  const el = document.createElement("button");
  el.className = className;
  el.textContent = label;
  el.addEventListener("click", onClick);
  return el;
}

async function send() {
  render("sending");
  try {
    const result = await api.runtime.sendMessage({ type: "send" });
    if (!result || !result.state) throw new Error("no response from the extension");
    render(result.state, { detail: result.detail, count: result.count });
  } catch (err) {
    render("worker-error", { detail: err.message });
  }
}

async function init() {
  try {
    const status = await api.runtime.sendMessage({ type: "status" });
    if (!status || !status.state) throw new Error("no response from the extension");
    render(status.state, status);
  } catch (err) {
    render("worker-error", { detail: err.message });
  }
}

init();
