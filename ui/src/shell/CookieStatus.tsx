import type { CookieStatus as CookieStatusValue } from "../api/enums";

// FRIENDLY_STATUS_LABELS maps every cookie status the backend sends to the
// label shown in the rail.
//
// It is typed as a TOTAL Record over CookieStatusValue, so the compiler now
// demands an entry for each. It previously listed an "expired" status the
// backend has never sent and omitted "stale", which it does send — yt-dlp
// writes "stale" when a cookie has aged out, and the rail rendered the bare
// word "stale" instead of a label. Naming the enum in Go (#196) and typing
// this map is what turns that into a build error rather than something a user
// discovers.
//
// "stale" and "blocked" deliberately read differently: stale means sign in
// again and re-export, blocked means YouTube is refusing this client and
// re-exporting will not help.
const FRIENDLY_STATUS_LABELS: Record<CookieStatusValue, string> = {
  valid: "active",
  absent: "missing",
  stale: "expired",
  blocked: "blocked",
};

// CookieStatus — the rail-foot indicator for the YouTube cookie's health,
// per the approved mockup's `.cookie` block. `status` is whatever
// cookieHealth().status / Settings.cookie_status returns from the backend;
// only "valid" reads as healthy — everything else defaults to the warning
// state.
export function CookieStatus({
  status,
  updatedAtLabel,
}: {
  status: string;
  updatedAtLabel?: string;
}) {
  const healthy = status === "valid";
  // Indexed as a plain string map on purpose: status arrives off the wire, so
  // an unrecognized value must still render (as itself, in the warning state)
  // rather than crash. The Record<CookieStatusValue, string> annotation above
  // is what makes the compiler demand an entry for every KNOWN status; this
  // widening only relaxes the lookup, never the totality check.
  const labels: Record<string, string> = FRIENDLY_STATUS_LABELS;
  const label = labels[status] ?? status;
  return (
    <div className={`cookie-status${healthy ? "" : " warn"}`}>
      <span className="led" aria-hidden="true" />
      <div>
        YouTube cookie <b>{label}</b>
        {updatedAtLabel ? (
          <>
            <br />
            <span className="cookie-detail">{updatedAtLabel}</span>
          </>
        ) : null}
      </div>
    </div>
  );
}
