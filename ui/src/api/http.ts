// http.ts — the shared fetch/JSON core every api/* module builds on. Ported
// from loom's api/http.ts (AuthExpiredError + expectJSON), extended with a
// generic `api` verb object (get/post/put/delete) since vark's domain
// modules are thin enough not to need loom's one-off-per-endpoint style.

// AuthExpiredError signals a 401 from an authenticated endpoint — the app
// treats it as "session expired" and should route to sign-in.
export class AuthExpiredError extends Error {
  constructor() {
    super("auth expired");
  }
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
export async function expectJSON<T>(response: Response, fallbackMessage: string): Promise<T> {
  if (response.status === 401) {
    throw new AuthExpiredError();
  }
  if (!response.ok) {
    throw new ApiError(response.status, await readErrorMessage(response, fallbackMessage));
  }
  return response.json() as Promise<T>;
}

async function readErrorMessage(response: Response, fallback: string): Promise<string> {
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
  async get<T>(path: string, fallbackMessage = `GET ${path} failed`): Promise<T> {
    const response = await fetch(path);
    return expectJSON<T>(response, fallbackMessage);
  },
  async post<T>(path: string, body?: unknown, fallbackMessage = `POST ${path} failed`): Promise<T> {
    const response = await fetch(path, jsonInit("POST", body));
    return expectJSON<T>(response, fallbackMessage);
  },
  async put<T>(path: string, body?: unknown, fallbackMessage = `PUT ${path} failed`): Promise<T> {
    const response = await fetch(path, jsonInit("PUT", body));
    return expectJSON<T>(response, fallbackMessage);
  },
  async delete<T>(path: string, fallbackMessage = `DELETE ${path} failed`): Promise<T> {
    const response = await fetch(path, { method: "DELETE" });
    return expectJSON<T>(response, fallbackMessage);
  },
};
