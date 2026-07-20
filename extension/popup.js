const api = globalThis.browser ?? globalThis.chrome;
const $ = (id) => document.getElementById(id);

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
};

function render(state, extra = {}) {
  const view = VIEWS[state] ?? VIEWS["server-error"];
  $("led").className = `led ${view.led === "idle" ? "" : view.led}`;
  $("headline").textContent = view.headline;
  $("detail").textContent = extra.detail || view.detail;
  $("host").textContent = extra.baseUrl ?? "";

  const facts = $("facts");
  facts.replaceChildren();
  // "Cookies sent" is only true after a successful send. Every other state
  // carrying a count is reporting what is PRESENT, so label it that way —
  // a no-session result must never read as though something was sent.
  const rows = [];
  if (state === "sent" && extra.count !== undefined) {
    rows.push(["Cookies sent", String(extra.count)]);
  } else if (extra.count !== undefined) {
    rows.push(["Sign-in cookies", `${extra.count} of ${extra.total ?? 5} present`]);
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
  const result = await api.runtime.sendMessage({ type: "send" });
  render(result.state, { detail: result.detail, count: result.count });
}

async function init() {
  const status = await api.runtime.sendMessage({ type: "status" });
  render(status.state, status);
}

init();
