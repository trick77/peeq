import { test } from "node:test";
import assert from "node:assert/strict";
import { normalizeBaseUrl, originOf, cookieEndpoint, loadConfig, saveConfig } from "./config.js";

// A minimal stand-in for chrome.storage.local: same promise-returning
// get/set shape the real API has under MV3.
function fakeStorage(initial = {}) {
  let data = { ...initial };
  return {
    async get(keys) {
      if (Array.isArray(keys)) {
        return Object.fromEntries(keys.filter((k) => k in data).map((k) => [k, data[k]]));
      }
      return { ...data };
    },
    async set(items) { data = { ...data, ...items }; },
    peek() { return { ...data }; },
  };
}

test("normalizeBaseUrl strips trailing slashes and whitespace", () => {
  assert.equal(normalizeBaseUrl("  https://peeq.home.lan/  "), "https://peeq.home.lan");
  assert.equal(normalizeBaseUrl("https://peeq.home.lan///"), "https://peeq.home.lan");
  assert.equal(normalizeBaseUrl("https://peeq.home.lan:8080"), "https://peeq.home.lan:8080");
});

test("normalizeBaseUrl keeps a path prefix", () => {
  assert.equal(normalizeBaseUrl("https://host.test/peeq/"), "https://host.test/peeq");
});

test("normalizeBaseUrl rejects junk and non-http schemes", () => {
  assert.throws(() => normalizeBaseUrl(""), /address/i);
  assert.throws(() => normalizeBaseUrl("not a url"), /address/i);
  assert.throws(() => normalizeBaseUrl("ftp://host.test"), /http/i);
  assert.throws(() => normalizeBaseUrl("javascript:alert(1)"), /http/i);
});

test("originOf produces a chrome.permissions match pattern", () => {
  assert.equal(originOf("https://peeq.home.lan"), "https://peeq.home.lan/*");
  assert.equal(originOf("https://peeq.home.lan:8080"), "https://peeq.home.lan:8080/*");
  // A path prefix must not leak into the origin pattern.
  assert.equal(originOf("https://host.test/peeq"), "https://host.test/*");
});

test("cookieEndpoint appends the machine route", () => {
  assert.equal(cookieEndpoint("https://peeq.home.lan"), "https://peeq.home.lan/api/machine/cookie");
  assert.equal(cookieEndpoint("https://host.test/peeq"), "https://host.test/peeq/api/machine/cookie");
});

test("loadConfig returns empty strings when nothing is stored", async () => {
  const cfg = await loadConfig(fakeStorage());
  assert.deepEqual(cfg, { baseUrl: "", token: "" });
});

test("saveConfig then loadConfig round-trips a normalized address", async () => {
  const storage = fakeStorage();
  await saveConfig(storage, { baseUrl: "https://peeq.home.lan/", token: "pq_secret" });
  assert.deepEqual(await loadConfig(storage), {
    baseUrl: "https://peeq.home.lan",
    token: "pq_secret",
  });
});

test("saveConfig never writes a cookie or any other key", async () => {
  const storage = fakeStorage();
  await saveConfig(storage, { baseUrl: "https://peeq.home.lan", token: "pq_secret" });
  assert.deepEqual(Object.keys(storage.peek()).sort(), ["baseUrl", "token"]);
});
