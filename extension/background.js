// Service worker: wires the real chrome.* APIs into the send pipeline and
// answers messages from the popup. It holds no state of its own.
import { sendCookie } from "./send.js";
import { loadConfig, originOf } from "./config.js";
import { selectYouTubeCookies, hasSessionCookie, countDisplayCookies, DISPLAY_COOKIE_NAMES } from "./shared.js";

const api = globalThis.browser ?? globalThis.chrome;

// Reads only THIS profile's store. Never getAllCookieStores(): merging stores
// can put two different sessions' __Secure-1PSID into one jar and let a dead
// session shadow a live one. The manifest's host_permissions must stay
// "*://*.youtube.com/*", not "https://*.youtube.com/*": Chrome filters
// chrome.cookies.getAll() results against host permissions by building each
// cookie's URL with scheme https only if that cookie has the Secure
// attribute, otherwise http. Non-Secure YouTube cookies such as SID, HSID
// and APISID would then resolve to an http:// URL that an https-only
// permission does not cover, silently hiding them from the extension.
const getCookies = () => api.cookies.getAll({ domain: ".youtube.com" });

async function realDeps() {
  const config = await loadConfig(api.storage.local);
  return {
    loadConfig: async () => config,
    getCookies,
    hasPermission: async () =>
      config.baseUrl ? api.permissions.contains({ origins: [originOf(config.baseUrl)] }) : false,
    fetch: (url, init) => fetch(url, init),
  };
}

async function status() {
  const config = await loadConfig(api.storage.local);
  if (!config.baseUrl || !config.token) return { state: "not-configured" };
  const cookies = selectYouTubeCookies(await getCookies());
  return {
    state: hasSessionCookie(cookies) ? "ready" : "no-session",
    baseUrl: config.baseUrl,
    count: countDisplayCookies(cookies),
    total: DISPLAY_COOKIE_NAMES.length,
  };
}

api.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  const handler = message?.type === "send" ? async () => sendCookie(await realDeps())
    : message?.type === "status" ? status
    : null;
  if (!handler) return false;
  // Deliberately not "server-error": that state belongs to the send pipeline
  // (a failed PUT). A rejection here can come from status() too (e.g.
  // chrome.cookies.getAll throwing), where nothing was ever sent.
  handler().then(sendResponse, (err) => sendResponse({ ok: false, state: "status-error", detail: String(err) }));
  return true; // keep the message channel open for the async response
});
