// The send pipeline, with every chrome.* dependency injected so it runs
// under plain Node in tests. background.js supplies the real ones.
import { toNetscape, selectYouTubeCookies, hasSessionCookie, countDisplayCookies } from "./shared.js";
import { cookieEndpoint } from "./config.js";

export async function sendCookie({ loadConfig, getCookies, hasPermission, fetch }) {
  const { baseUrl, token } = await loadConfig();
  if (!baseUrl || !token) {
    return { ok: false, state: "not-configured" };
  }

  const all = await getCookies();
  const cookies = selectYouTubeCookies(all);
  // Guard: an anonymous jar would overwrite peeq's good cookie with a
  // worthless one, so send nothing at all.
  if (!hasSessionCookie(cookies)) {
    return { ok: false, state: "no-session", count: countDisplayCookies(cookies) };
  }

  // peeq's origin is user-configured, so it can't be a static host_permission.
  // Without the runtime grant the PUT would be preflighted and peeq answers
  // no OPTIONS — so check first and report it as its own state.
  if (!(await hasPermission())) {
    return { ok: false, state: "permission-denied" };
  }

  let response;
  try {
    response = await fetch(cookieEndpoint(baseUrl), {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        // Bearer: auth/token_middleware.go compares with EqualFold. Note
        // TubeArchivist sends "Token <key>" — copying that 401s silently.
        Authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({ cookie: toNetscape(cookies) }),
    });
  } catch (err) {
    return { ok: false, state: "unreachable", detail: `Couldn't reach ${baseUrl}` };
  }

  if (response.status === 401) {
    return { ok: false, state: "rejected" };
  }
  if (!response.ok) {
    let detail = `peeq answered ${response.status}`;
    try {
      const body = await response.json();
      if (body && body.error) detail = body.error;
    } catch {
      // A non-JSON error body is not itself an error; keep the status line.
    }
    return { ok: false, state: "server-error", detail };
  }

  // A bare 200 is not evidence peeq received the cookie: a reverse proxy with
  // SPA fallback (common in self-hosting) answers unknown routes with 200 and
  // index.html, and some other service entirely could sit at that address.
  // Only peeq's own success body proves this. Require it before saying "sent".
  let body;
  try {
    body = await response.json();
  } catch {
    return {
      ok: false,
      state: "server-error",
      detail: "That address answered, but it doesn't look like peeq.",
    };
  }
  if (!body || body.status !== "valid") {
    return {
      ok: false,
      state: "server-error",
      detail: "That address answered, but it doesn't look like peeq.",
    };
  }

  // "sent" and "no-session" answer different questions and must not share a
  // count: this is how much was actually handed over (the full jar), not the
  // informational "N of 5" readout used above — don't unify them.
  return { ok: true, state: "sent", count: cookies.length };
}
