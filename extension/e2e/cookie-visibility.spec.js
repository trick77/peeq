// Browser-level regression test for the extension's cookie visibility.
//
// WHY THIS EXISTS. The extension shipped once with
// `host_permissions: ["https://*.youtube.com/*"]`. Chrome's cookies API filters
// getAll() results per cookie, building each cookie's URL with scheme `https`
// ONLY IF that cookie carries the Secure attribute — otherwise `http`. A
// non-Secure youtube.com cookie therefore mapped to `http://…`, which an
// https-only permission does not cover, and the extension simply could not see
// it.
//
// That mattered because real YouTube `SID` is NOT Secure. Measured from a real
// export of a signed-in account on .youtube.com:
//
//     FALSE  SID          <- one of the three cookies that GATE the send
//     FALSE  HSID
//     FALSE  APISID
//     TRUE   SSID, SAPISID, LOGIN_INFO, __Secure-1PSID, __Secure-3PSID
//
// The send would still pass its gate via __Secure-1PSID and report success
// while handing peeq a session missing SID/HSID/APISID — a silent, invisible
// degradation. No unit test could catch it: it lives entirely in the seam
// between manifest.json and Chrome's permission model.
//
// The second test re-creates the broken manifest and asserts the failure comes
// back, so this file can never become a test that cannot fail.
import { test, expect } from "@playwright/test";
import { chromium } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const EXTENSION_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Far-future expiry so the cookies are persistent rather than session cookies.
const EXPIRES = Math.floor(Date.now() / 1000) + 365 * 24 * 60 * 60;

// Secure flags mirror a real signed-in export — see the header. Do NOT "tidy"
// SID/HSID/APISID to secure:true: that over-tidy assumption in a fixture is
// precisely what hid the bug this file tests.
const YOUTUBE_COOKIES = [
  { name: "SID", value: "g.a000abc", secure: false, httpOnly: true },
  { name: "HSID", value: "g.a000hsid", secure: false, httpOnly: true },
  { name: "APISID", value: "g.a000apisid", secure: false, httpOnly: false },
  { name: "__Secure-1PSID", value: "g.a000def", secure: true, httpOnly: true },
  { name: "__Secure-3PSID", value: "g.a000ghi", secure: true, httpOnly: true },
  { name: "SAPISID", value: "sapi123", secure: true, httpOnly: false },
  { name: "LOGIN_INFO", value: "AFmmF2s", secure: true, httpOnly: true },
].map((c) => ({ ...c, domain: ".youtube.com", path: "/", expires: EXPIRES }));

/**
 * Launches Chrome with the extension loaded, seeds a signed-in YouTube cookie
 * jar and a configured peeq server, and returns the pieces a test needs.
 * Extensions load only into a persistent context, hence the temp profile.
 */
async function launchWithExtension(extensionDir) {
  const userDataDir = fs.mkdtempSync(path.join(os.tmpdir(), "peeq-e2e-"));
  const context = await chromium.launchPersistentContext(userDataDir, {
    // Extensions require Chrome's new headless mode; the old one ignored them.
    channel: "chromium",
    args: [
      `--disable-extensions-except=${extensionDir}`,
      `--load-extension=${extensionDir}`,
    ],
  });

  // The MV3 service worker may not have started at launch.
  let [worker] = context.serviceWorkers();
  if (!worker) worker = await context.waitForEvent("serviceworker");
  const extensionId = new URL(worker.url()).host;

  await context.addCookies(YOUTUBE_COOKIES);

  // Without a stored address and token the popup renders "Connect to peeq" and
  // never asks for cookie status, so seed a config. The value is irrelevant —
  // nothing in this file performs a send.
  await worker.evaluate(() =>
    chrome.storage.local.set({ baseUrl: "https://peeq.invalid", token: "pq_test" }),
  );

  return { context, worker, extensionId, cleanup: () => fs.rmSync(userDataDir, { recursive: true, force: true }) };
}

/** Cookie names the extension can actually see, via its own service worker. */
async function visibleCookieNames(worker) {
  const names = await worker.evaluate(async () => {
    const cookies = await chrome.cookies.getAll({ domain: ".youtube.com" });
    return cookies.map((c) => c.name);
  });
  return names.sort();
}

/** The popup's "Sign-in cookies: N of M present" readout. */
async function popupFacts(context, extensionId) {
  const page = await context.newPage();
  await page.goto(`chrome-extension://${extensionId}/popup.html`);
  // The popup asks the worker for status on load; wait for the real answer
  // rather than the "Checking…" placeholder.
  await expect(page.locator("#headline")).not.toHaveText("Checking…");
  return (await page.locator("#facts").textContent()) ?? "";
}

test("the extension sees non-Secure YouTube cookies, including SID", async () => {
  const { context, worker, extensionId, cleanup } = await launchWithExtension(EXTENSION_DIR);
  try {
    const names = await visibleCookieNames(worker);

    // The headline assertion: SID is not Secure, and it must still be visible.
    expect(names).toContain("SID");
    expect(names).toContain("HSID");
    expect(names).toContain("APISID");
    // Secure cookies were never in doubt, but a jar missing them would mean
    // something else broke entirely.
    expect(names).toContain("__Secure-1PSID");
    expect(names).toContain("LOGIN_INFO");

    // And the user-visible consequence: a fully signed-in profile reads 5 of 5.
    expect(await popupFacts(context, extensionId)).toContain("5 of 5 present");
  } finally {
    await context.close();
    cleanup();
  }
});

test("an https-only host permission hides SID — the shipped bug, kept reproducible", async () => {
  // Build a throwaway copy of the extension with the old, broken manifest.
  // Copying rather than mutating keeps the real manifest untouched even if this
  // test fails partway through.
  const brokenDir = fs.mkdtempSync(path.join(os.tmpdir(), "peeq-broken-ext-"));
  fs.cpSync(EXTENSION_DIR, brokenDir, {
    recursive: true,
    filter: (src) => !src.includes("node_modules") && !src.includes(`${path.sep}e2e`),
  });
  const manifestPath = path.join(brokenDir, "manifest.json");
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  manifest.host_permissions = ["https://*.youtube.com/*"];
  fs.writeFileSync(manifestPath, JSON.stringify(manifest, null, 2));

  const { context, worker, extensionId, cleanup } = await launchWithExtension(brokenDir);
  try {
    const names = await visibleCookieNames(worker);

    // The bug, reproduced: non-Secure cookies are invisible.
    expect(names).not.toContain("SID");
    expect(names).not.toContain("HSID");
    expect(names).not.toContain("APISID");
    // Secure ones still come through, which is exactly why this degraded
    // silently — the send gate passes on __Secure-1PSID alone.
    expect(names).toContain("__Secure-1PSID");

    // The user-visible symptom was a count quietly one short, never an error.
    expect(await popupFacts(context, extensionId)).toContain("4 of 5 present");
  } finally {
    await context.close();
    cleanup();
    fs.rmSync(brokenDir, { recursive: true, force: true });
  }
});
