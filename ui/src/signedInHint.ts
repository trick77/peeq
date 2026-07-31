const SIGNED_IN_HINT_KEY = "peeq.signedIn";

/**
 * Was this browser signed in the last time the app checked?
 *
 * Nothing here is authority — the cookie is, and only /api/auth/me can read it.
 * This is a hint about what the answer is *going to be*, and it exists for one
 * frame: the one between first paint and that request landing.
 *
 * Without it a returning user gets the full sign-in screen — cover art, lockup,
 * tagline — flashed at them on every reload, then yanked away the moment the
 * session checks out. The card itself is right for a genuine first visit (see
 * SignIn's `checking` prop, and #294 for why it is a card and not a bare
 * wordmark), so the fix is not to weaken that screen; it is to know, before
 * asking, that this particular visitor is not the one it was designed for.
 *
 * Guarded throughout: App also renders through renderToStaticMarkup with no
 * window at all, and a browser can refuse storage. A missing hint is the safe
 * direction — it only costs the flash it was added to remove.
 */
export function readSignedInHint(): boolean {
  try {
    return (
      typeof window !== "undefined" &&
      window.localStorage?.getItem(SIGNED_IN_HINT_KEY) === "1"
    );
  } catch {
    return false;
  }
}

/**
 * Record what the session check just answered.
 *
 * Call only for an answer that came back: signed in, or a 401 that says the
 * session is genuinely gone. A getMe() that *threw* — backend down, network
 * dropped — must leave the hint exactly as it is. Clearing on an unreachable
 * server would mean the next reload flashes the very screen this removes, at
 * the one moment the user is least able to act on it.
 */
export function writeSignedInHint(signedIn: boolean) {
  try {
    if (signedIn) {
      window.localStorage?.setItem(SIGNED_IN_HINT_KEY, "1");
    } else {
      window.localStorage?.removeItem(SIGNED_IN_HINT_KEY);
    }
  } catch {
    /* storage blocked — the session still works, it just forgets. */
  }
}
