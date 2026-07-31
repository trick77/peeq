import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { takeAuthFailed } from "./authError";

// Each case starts from a clean URL; replaceState is how the function strips
// the flag, so the assertions read it back off window.location.
function at(url: string) {
  window.history.replaceState(null, "", url);
}

describe("takeAuthFailed", () => {
  beforeEach(() => at("/"));
  afterEach(() => {
    vi.restoreAllMocks();
    at("/");
  });

  it("is false on an ordinary load", () => {
    expect(takeAuthFailed()).toBe(false);
  });

  it("is true when the backend bounced us back with auth_error", () => {
    at("/?auth_error=oidc_callback_failed");
    expect(takeAuthFailed()).toBe(true);
  });

  // The flag describes one navigation. Left in the address bar it would
  // re-assert the failure on every later reload of the same tab.
  it("strips the flag from the URL", () => {
    at("/?auth_error=oidc_callback_failed");
    takeAuthFailed();
    expect(window.location.search).toBe("");
  });

  it("is false a second time, having consumed the flag", () => {
    at("/?auth_error=oidc_callback_failed");
    takeAuthFailed();
    expect(takeAuthFailed()).toBe(false);
  });

  // peeq has no router, but a share link carries its token in the path and the
  // player its video in the query — stripping the wrong thing would break both.
  it("leaves every other query parameter alone", () => {
    at("/?v=abc&auth_error=oidc_callback_failed&t=90");
    expect(takeAuthFailed()).toBe(true);
    expect(window.location.search).toBe("?v=abc&t=90");
  });

  // A browser that refuses history access must cost an error message, not the
  // whole app.
  it("is false when history access throws", () => {
    at("/?auth_error=oidc_callback_failed");
    vi.spyOn(window.history, "replaceState").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(takeAuthFailed()).toBe(false);
  });
});
