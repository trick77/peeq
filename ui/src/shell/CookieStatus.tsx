// FRIENDLY_STATUS_LABELS maps the raw cookie_status/cookieHealth().status
// values the backend sends ("valid" | "absent" | "expired" | "blocked") to
// the label shown in the rail. Any status not listed here (a value we don't
// yet know about) falls back to showing the raw string itself, still in the
// warning state — the safe default for an unrecognized status.
const FRIENDLY_STATUS_LABELS: Record<string, string> = {
  valid: "active",
  absent: "missing",
  expired: "expired",
  blocked: "blocked",
};

// CookieStatus — the rail-foot indicator for the YouTube cookie's health,
// per the approved mockup's `.cookie` block. `status` is whatever
// cookieHealth().status / Settings.cookie_status returns from the backend;
// only "valid" reads as healthy — everything else defaults to the warning
// state.
export function CookieStatus({ status, updatedAtLabel }: { status: string; updatedAtLabel?: string }) {
  const healthy = status === "valid";
  const label = FRIENDLY_STATUS_LABELS[status] ?? status;
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
