/**
 * Did this page load arrive from a failed OIDC callback?
 *
 * The backend redirects to /?auth_error=oidc_callback_failed when it cannot
 * complete the callback (see auth_handlers.go). Nothing on the client read it,
 * so the round trip ended on a sign-in screen identical to the one the user had
 * just left — indistinguishable from the button having done nothing.
 *
 * "take", not "read": the flag is consumed. It describes one navigation, and
 * leaving it in the address bar would re-assert the failure on every later
 * reload of the same tab — including the reload that follows a *successful*
 * sign-in, if the user got there by going back. Stripping it with
 * replaceState also keeps it out of the history entry.
 *
 * Call once, at module load, before anything else can rewrite the URL. The
 * whole body is guarded: App also renders through renderToStaticMarkup in
 * App.test.tsx, where there is no window at all, and a browser can refuse
 * history access. Neither is worth a blank app over an error message.
 */
export function takeAuthFailed(): boolean {
  try {
    if (typeof window === "undefined") return false;
    const url = new URL(window.location.href);
    if (!url.searchParams.has("auth_error")) return false;
    url.searchParams.delete("auth_error");
    window.history.replaceState(null, "", url.toString());
    return true;
  } catch {
    return false;
  }
}
