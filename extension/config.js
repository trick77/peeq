// Persisted settings. The extension stores peeq's address and the API token
// and NOTHING else — never a cookie, which is read fresh on every send and
// discarded immediately after the request.

const KEYS = ["baseUrl", "token"];

export function normalizeBaseUrl(input) {
  const trimmed = String(input ?? "").trim();
  if (trimmed === "") {
    throw new Error("Enter peeq's address, for example https://peeq.home.lan");
  }
  let url;
  try {
    url = new URL(trimmed);
  } catch {
    throw new Error("That doesn't look like an address. Try https://peeq.home.lan");
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new Error("peeq's address must start with http:// or https://");
  }
  return (url.origin + url.pathname).replace(/\/+$/, "");
}

// chrome.permissions wants an origin match pattern; a configured path prefix
// is not part of the origin and must not appear here.
export function originOf(baseUrl) {
  return new URL(baseUrl).origin + "/*";
}

export function cookieEndpoint(baseUrl) {
  return `${baseUrl}/api/machine/cookie`;
}

export async function loadConfig(storage) {
  const stored = await storage.get(KEYS);
  return { baseUrl: stored.baseUrl ?? "", token: stored.token ?? "" };
}

export async function saveConfig(storage, { baseUrl, token }) {
  await storage.set({ baseUrl: normalizeBaseUrl(baseUrl), token });
}
