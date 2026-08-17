import { Spinner, buttonClass } from "../ui";

/**
 * The mark, at the one size it is ever drawn here.
 *
 * Same geometry as the favicon (ui/icons/icon.svg), which carries the full
 * reasoning: lucide `square-play` inverted — the chip filled with the accent
 * gradient and the wedge cut out of it in --color-bg, rather than an orange
 * stroke framing an orange wedge. The viewBox is the chip's true bbox, 3 3 18
 * 18, not the nominal 24x24, which is two thirds empty and would draw the mark
 * small inside its own box; it lost the half-stroke padding it used to carry
 * because a filled chip's drawn edge is its geometric edge.
 *
 * The wedge is scaled 1.35 about the chip's centre. lucide sized it to sit
 * inside a stroked frame, and on a filled chip that untouched wedge covers only
 * 39%, which reads as an orange square with a speck in it. Do not push it
 * further without re-centring: the wedge's own centre is x 12.45, not 12.
 *
 * The rail dropped its copy of this in #286 (loom's header carries a word and
 * its controls, nothing else). Sign-in is the one screen with no chrome around
 * it, so the lockup is the only thing naming the app, and the mark earns its
 * place back. The accent span that painted "ee" does not: it was retired with
 * the rail's lockup and is not coming back through a side door.
 */
function SignInMark() {
  return (
    <svg className="signin-logo" viewBox="3 3 18 18" aria-hidden="true">
      <linearGradient id="signin-mark-grad" x1="0" y1="0" x2="0.72" y2="1">
        <stop className="s0" offset="0" />
        <stop className="s1" offset="1" />
      </linearGradient>
      <rect
        x="3"
        y="3"
        width="18"
        height="18"
        rx="4"
        fill="url(#signin-mark-grad)"
      />
      <path
        d="M9 9.003a1 1 0 0 1 1.517-.859l4.997 2.997a1 1 0 0 1 0 1.718l-4.997 2.997A1 1 0 0 1 9 14.996z"
        fill="var(--color-bg)"
        transform="translate(-4.2 -4.2) scale(1.35)"
      />
    </svg>
  );
}

export type SignInProps = {
  /**
   * The session check has not come back yet. The card still renders — swapping
   * a bare wordmark for a full card the instant /api/auth/me answers is a
   * visible jolt on every cold load — but the action slot spins instead of
   * offering a button there is no answer for yet.
   */
  checking?: boolean;
  /**
   * getMe() threw rather than returning 401: the backend is down, or the
   * network is. Distinct from a failed sign-in, and the copy says so — "try
   * reloading" is the useful advice, not "try again".
   */
  unreachable?: boolean;
  /**
   * The OIDC callback failed and the backend bounced us back to `/` with
   * ?auth_error=. Without this the round trip ends on an identical blank
   * sign-in screen, which reads as "the button did nothing".
   */
  failed?: boolean;
};

/**
 * The signed-out screen: the whole viewport, no rail and no top bar.
 *
 * Every visible surface is the app's own — .ui-btn for the action, .errline for
 * a failure, --color-panel and --radius-ui-lg for the card. The only thing here
 * that is not is the cover art, and it is deliberately kept to the role of
 * ground: contrast for the text comes from the card's opaque fill, never from
 * the scrim over the image, so the picture can stay bright without the sign-in
 * ever being dimmed to compensate.
 */
export function SignIn({ checking, unreachable, failed }: SignInProps) {
  return (
    <div className="signin">
      <img className="signin-cover" src="/signin-cover.webp" alt="" />
      <div className="signin-card">
        <div className="signin-brand">
          <SignInMark />
          <b>Peeq</b>
        </div>
        <p className="signin-tag">
          Your subscriptions, downloaded, transcribed and searchable.
        </p>
        {unreachable ? (
          <div className="errline" role="alert">
            Couldn't reach the server. Try reloading.
          </div>
        ) : null}
        {failed && !unreachable ? (
          <div className="errline" role="alert">
            Sign-in didn't complete. Try again.
          </div>
        ) : null}
        {checking ? (
          // Height-matched to the button below it so the swap does not move
          // anything. aria-live, because a spinner replacing a control is a
          // state change a screen reader has no other way to hear.
          <div className="signin-wait" role="status" aria-live="polite">
            <Spinner />
            <span>Checking your session</span>
          </div>
        ) : (
          <a
            className={`${buttonClass("primary")} signin-go`}
            href="/api/auth/login"
          >
            Sign in
          </a>
        )}
      </div>
    </div>
  );
}
