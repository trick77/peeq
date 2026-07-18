// CookieStatus — the rail-foot indicator for the YouTube cookie's health,
// per the approved mockup's `.cookie` block. `status` is whatever
// cookieHealth().status / Settings.cookie_status returns from the backend
// ("valid" | "absent" | "expired" | ...); only "valid" reads as healthy —
// everything else (including values we don't yet know about) defaults to
// the warning state, which is the safe branch when a new status appears.
export function CookieStatus({ status, updatedAtLabel }: { status: string; updatedAtLabel?: string }) {
  const healthy = status === "valid";
  return (
    <div className={`cookie-status${healthy ? "" : " warn"}`}>
      <span className="led" aria-hidden="true" />
      <div>
        YouTube cookie <b>{healthy ? "active" : status}</b>
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
