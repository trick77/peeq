import { loadConfig, saveConfig, normalizeBaseUrl, originOf } from "./config.js";

const api = globalThis.browser ?? globalThis.chrome;
const $ = (id) => document.getElementById(id);

function verdict(kind, text) {
  $("verdict").hidden = false;
  $("led").className = `led ${kind}`;
  $("verdictText").textContent = text;
}

async function init() {
  const { baseUrl, token } = await loadConfig(api.storage.local);
  $("baseUrl").value = baseUrl;
  $("token").value = token;
}

$("save").addEventListener("click", async () => {
  let normalized;
  try {
    normalized = normalizeBaseUrl($("baseUrl").value);
  } catch (err) {
    verdict("bad", err.message);
    return;
  }
  if (!$("token").value.trim()) {
    verdict("bad", "Paste the access token from peeq's Settings page.");
    return;
  }

  // Must be called from the click itself: chrome.permissions.request requires
  // a user gesture, so it cannot be moved after an await of something slow.
  let granted;
  try {
    granted = await api.permissions.request({ origins: [originOf(normalized)] });
  } catch (err) {
    verdict("bad", `Chrome refused the permission request: ${err.message}`);
    return;
  }
  if (!granted) {
    verdict("bad", "Chrome permission denied. Without it the extension can't reach peeq.");
    return;
  }

  await saveConfig(api.storage.local, { baseUrl: normalized, token: $("token").value.trim() });
  verdict("ok", "Connected. Open the extension to send the cookie.");
});

init();
