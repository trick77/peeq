// http.ts — the shared fetch/JSON core every api/* module builds on. Ported
// from loom's api/http.ts (AuthExpiredError + expectJSON), extended with a
// generic `api` verb object (get/post/put/delete) since peeq's domain
// modules are thin enough not to need loom's one-off-per-endpoint style.

// AuthExpiredError signals a 401 from an authenticated endpoint — the app
// treats it as "session expired" and routes to sign-in. The routing happens
// through the listener below, not through callers: every view catches errors
// to show a message, and none of them is the right place to know about
// sessions. So the throw is for the caller that wants to stop what it was
// doing; the listener is for the shell that owns the user.
export class AuthExpiredError extends Error {
  constructor() {
    super("auth expired");
  }
}

const authExpiredListeners = new Set<() => void>();

// onAuthExpired registers fn to run on any 401 and returns its unsubscribe.
// The shell (useAuthBootstrap) is the one listener that matters, but a set
// means no later registration can silently pre-empt it.
export function onAuthExpired(fn: () => void): () => void {
  authExpiredListeners.add(fn);
  return () => {
    authExpiredListeners.delete(fn);
  };
}

// notifyAuthExpired runs every registered listener. A no-op when nobody
// listens (static render, module tests). Exported for the shell's tests; the
// api modules go through throwAuthExpired so a 401 can never be mapped
// without the notification.
export function notifyAuthExpired(): void {
  for (const fn of authExpiredListeners) fn();
}

// throwAuthExpired is the ONE way a 401 becomes an AuthExpiredError: notify
// the shell, then throw for the caller. Every 401 mapping (here, stream.ts,
// and any raw fetch a component still does) goes through it.
export function throwAuthExpired(): never {
  notifyAuthExpired();
  throw new AuthExpiredError();
}

// ApiError is thrown for any other non-2xx response. It carries the HTTP
// status so callers can branch on specific codes (e.g. 409 cookie-required,
// 400 bad request) without re-parsing the message.
export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// expectJSON is the shared success/JSON path: it maps a 401 to
// AuthExpiredError, any other non-2xx to ApiError (message read from the
// body's `error` field when present), and otherwise decodes the JSON body.
export async function expectJSON<T>(
  response: Response,
  fallbackMessage: string,
): Promise<T> {
  if (response.status === 401) {
    throwAuthExpired();
  }
  if (!response.ok) {
    throw new ApiError(
      response.status,
      await readErrorMessage(response, fallbackMessage),
    );
  }
  return response.json() as Promise<T>;
}

// expectNoContent is expectJSON's sibling for endpoints that reply 2xx with
// an empty (or irrelevant) body — e.g. 202 Accepted for a fire-and-forget
// job. It preserves the same 401 -> AuthExpiredError / non-2xx -> ApiError
// mapping but deliberately never calls response.json() on the success path,
// so an empty body doesn't throw a SyntaxError.
export async function expectNoContent(
  response: Response,
  fallbackMessage: string,
): Promise<void> {
  if (response.status === 401) {
    throwAuthExpired();
  }
  if (!response.ok) {
    throw new ApiError(
      response.status,
      await readErrorMessage(response, fallbackMessage),
    );
  }
}

async function readErrorMessage(
  response: Response,
  fallback: string,
): Promise<string> {
  try {
    const body = (await response.json()) as { error?: unknown };
    if (typeof body.error === "string" && body.error !== "") {
      return body.error;
    }
  } catch {
    // response body was empty or not JSON
  }
  return fallback;
}

function jsonInit(method: string, body?: unknown): RequestInit {
  const init: RequestInit = { method };
  if (body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }
  return init;
}

// api — the generic verb surface used by every domain module (videos,
// downloads, settings, auth). Each call decodes JSON on 2xx, throws
// AuthExpiredError on 401, and ApiError otherwise.
export const api = {
  async get<T>(
    path: string,
    fallbackMessage = `GET ${path} failed`,
  ): Promise<T> {
    const response = await fetch(path);
    return expectJSON<T>(response, fallbackMessage);
  },
  async post<T>(
    path: string,
    body?: unknown,
    fallbackMessage = `POST ${path} failed`,
  ): Promise<T> {
    const response = await fetch(path, jsonInit("POST", body));
    return expectJSON<T>(response, fallbackMessage);
  },
  // postNoContent is for endpoints that reply 2xx with an empty body (e.g.
  // 202 Accepted for a queued job) — see expectNoContent above for why this
  // must not decode JSON on the success path.
  async postNoContent(
    path: string,
    body?: unknown,
    fallbackMessage = `POST ${path} failed`,
  ): Promise<void> {
    const response = await fetch(path, jsonInit("POST", body));
    return expectNoContent(response, fallbackMessage);
  },
  async put<T>(
    path: string,
    body?: unknown,
    fallbackMessage = `PUT ${path} failed`,
  ): Promise<T> {
    const response = await fetch(path, jsonInit("PUT", body));
    return expectJSON<T>(response, fallbackMessage);
  },
  async delete<T>(
    path: string,
    fallbackMessage = `DELETE ${path} failed`,
  ): Promise<T> {
    const response = await fetch(path, { method: "DELETE" });
    return expectJSON<T>(response, fallbackMessage);
  },
};
