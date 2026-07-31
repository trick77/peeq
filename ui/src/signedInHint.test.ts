import { describe, it, expect, afterEach } from "vitest";
import { readSignedInHint, writeSignedInHint } from "./signedInHint";

// This environment's global localStorage is node's experimental one, which has
// no clear() and is not the browser object the module talks to. A tiny map
// stands in for it, which also keeps one case's write out of the next.
function stubStorage(seed?: string) {
  const map = new Map<string, string>();
  if (seed !== undefined) map.set("peeq.signedIn", seed);
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    value: {
      getItem: (k: string) => map.get(k) ?? null,
      setItem: (k: string, v: string) => void map.set(k, v),
      removeItem: (k: string) => void map.delete(k),
    },
  });
  return map;
}

function stubBrokenStorage() {
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    value: {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("denied");
      },
      removeItem: () => {
        throw new Error("denied");
      },
    },
  });
}

describe("signedInHint", () => {
  afterEach(() => stubStorage());

  it("is false for a browser that has never signed in", () => {
    stubStorage();
    expect(readSignedInHint()).toBe(false);
  });

  it("reads back what a signed-in check wrote", () => {
    stubStorage();
    writeSignedInHint(true);
    expect(readSignedInHint()).toBe(true);
  });

  // A 401 is an answer: the session is really gone, and the next reload should
  // expect the sign-in screen rather than suppress it.
  it("forgets when the check comes back signed out", () => {
    const store = stubStorage("1");
    writeSignedInHint(false);
    expect(store.has("peeq.signedIn")).toBe(false);
    expect(readSignedInHint()).toBe(false);
  });

  it("treats any other stored value as no hint", () => {
    stubStorage("yes");
    expect(readSignedInHint()).toBe(false);
  });

  // Storage blocked costs the flash the hint removes, never the app.
  it("survives storage that throws", () => {
    stubBrokenStorage();
    expect(readSignedInHint()).toBe(false);
    expect(() => writeSignedInHint(true)).not.toThrow();
    expect(() => writeSignedInHint(false)).not.toThrow();
  });
});
