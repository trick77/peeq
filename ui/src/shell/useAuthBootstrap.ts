import { useEffect, useState } from "react";
import { getMe } from "../api";
import { onAuthExpired } from "../api/http";
import { readSignedInHint, writeSignedInHint } from "../signedInHint";
import type { User } from "../api/types";

// How long a session check may take before a hinted browser stops being shown
// nothing and gets the checking card instead. Long enough that a healthy
// backend never reaches it — the flash the hint removes lasts a fraction of
// this — short enough that a slow or hung one is never a blank page.
export const SLOW_CHECK_MS = 600;

export type AuthBootstrap = {
  user: User | null;
  // True once /api/auth/me has answered or failed. Never goes back to false:
  // an expiry mid-use is a known answer ("signed out"), not a new check.
  authChecked: boolean;
  // getMe() rejected: backend down, network error, or a non-401 failure. Not
  // set on a 401, which is a plain "no session".
  authError: boolean;
  // The check has run longer than SLOW_CHECK_MS without answering.
  checkSlow: boolean;
  // What storage said this browser should expect, read once on mount. The
  // check's answer updates storage, not this value — see readSignedInHint.
  signedInHint: boolean;
  // The session was live and then a request came back 401. Lets the sign-in
  // card say why it is there instead of reading as a cold visit.
  expired: boolean;
};

// useAuthBootstrap owns the session check and the one thing that can undo it:
// a 401 from any later request. Every api call maps a 401 to AuthExpiredError
// and notifies the listener registered here, so the shell returns to the
// sign-in card without any view having to know about sessions.
export function useAuthBootstrap(): AuthBootstrap {
  const [user, setUser] = useState<User | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [signedInHint] = useState(readSignedInHint);
  const [authError, setAuthError] = useState(false);
  const [checkSlow, setCheckSlow] = useState(false);
  const [expired, setExpired] = useState(false);

  useEffect(() => {
    let active = true;
    getMe()
      .then((u) => {
        if (active) setUser(u);
        // Resolving at all means the server answered — a user, or a 401 that
        // says the session is really gone. Either way the hint now knows what
        // the next reload should expect. Deliberately not in .catch(): see
        // writeSignedInHint.
        writeSignedInHint(!!u);
      })
      .catch(() => {
        // getMe() rejecting (backend down, network error, or a non-401
        // failure the client surfaced as a thrown error) must never become
        // an unhandled rejection — treat it the same as "not signed in".
        if (active) setAuthError(true);
      })
      .finally(() => {
        if (active) setAuthChecked(true);
      });
    return () => {
      active = false;
    };
  }, []);

  // The hint buys silence for a quick check, not for an arbitrarily long one.
  // /api/auth/me can take seconds on a cold-started backend, and can hang
  // outright on a server that accepts the connection and never answers —
  // getMe() has no timeout of its own. Painting nothing for that whole stretch
  // trades a brief flash for a page that looks broken, so once the check stops
  // being quick the checking card comes back and says what is happening.
  useEffect(() => {
    if (authChecked) return;
    const timer = setTimeout(() => setCheckSlow(true), SLOW_CHECK_MS);
    return () => clearTimeout(timer);
  }, [authChecked]);

  // A 401 mid-use: the session is gone. Clearing the user swaps the whole
  // tree for the sign-in card on the next render; clearing the hint keeps the
  // next reload from painting nothing while it waits for the same answer.
  useEffect(() => {
    return onAuthExpired(() => {
      setUser(null);
      setExpired(true);
      writeSignedInHint(false);
    });
  }, []);

  return { user, authChecked, authError, checkSlow, signedInHint, expired };
}
