import { test } from "node:test";
import assert from "node:assert/strict";
import { sendCookie } from "./send.js";

const ytCookie = (name, extra = {}) => ({
  domain: ".youtube.com", path: "/", name, value: "v",
  secure: true, httpOnly: true, expirationDate: 1819099943.5, ...extra,
});

const SIGNED_IN = [ytCookie("SID"), ytCookie("__Secure-1PSID"), ytCookie("PREF")];

function deps({
  config = { baseUrl: "https://peeq.home.lan", token: "pq_tok" },
  cookies = SIGNED_IN,
  hasPermission = true,
  fetchImpl = async () => new Response(JSON.stringify({ status: "valid", present: true }), { status: 200 }),
} = {}) {
  const calls = [];
  return {
    calls,
    loadConfig: async () => config,
    getCookies: async () => cookies,
    hasPermission: async () => hasPermission,
    fetch: async (url, init) => { calls.push({ url, init }); return fetchImpl(url, init); },
  };
}

test("a signed-in jar is serialized and PUT with a Bearer token", async () => {
  const d = deps();
  const result = await sendCookie(d);

  assert.equal(result.ok, true);
  assert.equal(result.state, "sent");
  assert.equal(result.count, 3);
  assert.equal(d.calls.length, 1);

  const { url, init } = d.calls[0];
  assert.equal(url, "https://peeq.home.lan/api/machine/cookie");
  assert.equal(init.method, "PUT");
  // Bearer, NOT "Token" — TubeArchivist uses Token and it would 401 silently.
  assert.equal(init.headers.Authorization, "Bearer pq_tok");
  assert.equal(init.headers["Content-Type"], "application/json");

  const body = JSON.parse(init.body);
  assert.ok(body.cookie.includes("#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1819099943\tSID\tv"));
  assert.ok(!("cookies" in body), "the API field is `cookie`, singular");
});

test("a jar with no session cookie is never sent", async () => {
  // Sending an anonymous jar would overwrite peeq's good cookie.
  const d = deps({ cookies: [ytCookie("PREF"), ytCookie("VISITOR_INFO1_LIVE")] });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "no-session");
  assert.equal(d.calls.length, 0, "no request may be made without a session cookie");
});

test("missing configuration short-circuits before any request", async () => {
  const d = deps({ config: { baseUrl: "", token: "" } });
  const result = await sendCookie(d);

  assert.equal(result.state, "not-configured");
  assert.equal(d.calls.length, 0);
});

test("a missing host permission is reported distinctly from unreachable", async () => {
  // Both surface as a failed fetch but need opposite fixes, so they must not
  // be collapsed into one state.
  const d = deps({ hasPermission: false });
  const result = await sendCookie(d);

  assert.equal(result.state, "permission-denied");
  assert.equal(d.calls.length, 0, "must not attempt a fetch without permission");
});

test("a network failure reports unreachable and names the address", async () => {
  const d = deps({ fetchImpl: async () => { throw new TypeError("Failed to fetch"); } });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "unreachable");
  assert.ok(result.detail.includes("peeq.home.lan"),
    "a typo in the address looks identical to a server being down, so name it");
});

test("401 reports a rejected token", async () => {
  const d = deps({ fetchImpl: async () => new Response("unauthorized", { status: 401 }) });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "rejected");
});

test("a 400 from cookie validation is a server-error carrying peeq's reason", async () => {
  // peeq accepted the request but refused the cookie — the user needs the why.
  const d = deps({
    fetchImpl: async () => new Response(JSON.stringify({ error: "invalid cookie: no session" }), { status: 400 }),
  });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "server-error");
  assert.ok(result.detail.includes("no session"));
});

test("a 200 with an HTML body is not reported as sent", async () => {
  // A reverse proxy with SPA fallback answers unknown routes with 200 and
  // index.html — that must not be mistaken for peeq's success.
  const d = deps({
    fetchImpl: async () => new Response("<!doctype html><html><body>not peeq</body></html>", { status: 200 }),
  });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "server-error");
  assert.ok(result.detail.includes("doesn't look like peeq"));
});

test("a 200 with valid JSON but the wrong shape is not reported as sent", async () => {
  const d = deps({
    fetchImpl: async () => new Response(JSON.stringify({ ok: true }), { status: 200 }),
  });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.notEqual(result.state, "sent");
  assert.equal(result.state, "server-error");
});

test("the cookie body is not retained on the result", async () => {
  const result = await sendCookie(deps());
  assert.ok(!JSON.stringify(result).includes("__Secure-1PSID"),
    "the extension must never hold on to cookie material");
});
