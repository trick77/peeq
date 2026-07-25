import { useEffect, useRef, useState, type ReactNode } from "react";
import { Icon } from "../icons";
import { Button } from "../ui";
import {
  createShare,
  stopShare,
  type ShareStatus,
  type ShareTTL,
} from "../api";
import { DOT } from "../sep";

// The fixed lifetimes the popover offers, in the order they read on screen.
const TTLS: { id: ShareTTL; label: string }[] = [
  { id: "24h", label: "24h" },
  { id: "7d", label: "7 days" },
  { id: "30d", label: "30 days" },
  { id: "never", label: "Never" },
];

// daysUntil parses a UTC 'YYYY-MM-DD HH:MM:SS' expiry and returns whole days
// remaining (rounded up), or null when the link never expires.
export function daysUntil(expiresAt: string | undefined): number | null {
  if (!expiresAt) return null;
  const exp = new Date(expiresAt.replace(" ", "T") + "Z").getTime();
  if (Number.isNaN(exp)) return null;
  return Math.max(0, Math.ceil((exp - Date.now()) / 86_400_000));
}

// shareChipLabel is the wording on the "Shared" chip beside the player title.
export function shareChipLabel(status: ShareStatus): string {
  const d = daysUntil(status.expires_at);
  if (d === null) return "Shared";
  if (d <= 0) return `Shared${DOT}expires today`;
  if (d === 1) return `Shared${DOT}1 day left`;
  return `Shared${DOT}${d} days left`;
}

// ShareChip is the at-a-glance "this video is public" signal beside the title.
// Green normally; amber in the final day so an expiry never surprises the owner.
export function ShareChip({ status }: { status: ShareStatus }) {
  if (!status.shared) return null;
  const d = daysUntil(status.expires_at);
  const soon = d !== null && d <= 1;
  return (
    <span className={`share-chip${soon ? " soon" : ""}`}>
      <Icon name="share" size="12px" />
      {shareChipLabel(status)}
    </span>
  );
}

// bucketTtl picks which chip reads as selected when the popover opens on an
// already-shared link, mapping the remaining lifetime back to the nearest
// preset. A never-shared video defaults to 7 days.
function bucketTtl(status: ShareStatus): ShareTTL {
  if (!status.shared) return "7d";
  const d = daysUntil(status.expires_at);
  if (d === null) return "never";
  if (d <= 1) return "24h";
  if (d <= 7) return "7d";
  if (d <= 30) return "30d";
  return "never";
}

// absoluteUrl turns the API's link (which may be a bare /s/<token> path when
// BACKEND_PUBLIC_URL is unset) into a full URL the recipient can open.
function absoluteUrl(url: string): string {
  if (/^https?:\/\//.test(url)) return url;
  return window.location.origin + url;
}

// prettyUrl drops the scheme for a more compact display in the link field.
function prettyUrl(url: string | undefined): string {
  if (!url) return "";
  return absoluteUrl(url).replace(/^https?:\/\//, "");
}

type Props = {
  videoId: string;
  status: ShareStatus;
  onStatusChange: (s: ShareStatus) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The trigger the popover hangs off — the Player passes its ⋮ menu. */
  children: ReactNode;
};

// ShareControl is the anchored share popover: create a link, pick a lifetime,
// copy it, or stop sharing. Chosen over a modal so the action stays light — no
// dimmed backdrop, closes on an outside click.
//
// It owns no trigger of its own. The caller opens it (from the ⋮ menu) and
// passes that trigger as children, so the trigger sits *inside* the wrapper the
// outside-click handler below tests against — otherwise the very click that
// opens the popover would read as an outside click and close it again.
export function ShareControl({
  videoId,
  status,
  onStatusChange,
  open,
  onOpenChange,
  children,
}: Props) {
  const [ttl, setTtl] = useState<ShareTTL>(() => bucketTtl(status));
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const [armed, setArmed] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const wrapRef = useRef<HTMLSpanElement | null>(null);
  const popRef = useRef<HTMLDivElement | null>(null);
  const copyTimer = useRef<number | undefined>(undefined);

  // Re-seed the selected chip when the status changes. status often arrives
  // asynchronously (the Player mounts this with {shared:false} and fills it in
  // after getShareStatus resolves), so a mount-only seed would leave the popover
  // showing "7 days" for a link that is actually, say, a 30-day one.
  useEffect(() => {
    setTtl(bucketTtl(status));
  }, [status]);

  // Close on an outside click or Escape.
  useEffect(() => {
    if (!open) return;
    function onDoc(e: MouseEvent) {
      if (!wrapRef.current?.contains(e.target as Node)) onOpenChange(false);
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onOpenChange(false);
    }
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, onOpenChange]);

  // Pull focus into the dialog when it opens. RowMenu returns focus to the ⋮
  // before it fires the action, so without this the popover would open with the
  // keyboard still parked on the trigger behind it.
  useEffect(() => {
    if (!open) return;
    popRef.current?.querySelector<HTMLElement>("button")?.focus();
  }, [open]);

  // Reset the transient popover state each time it closes.
  useEffect(() => {
    if (!open) {
      setCopied(false);
      setArmed(false);
      setError(null);
    }
  }, [open]);

  useEffect(() => () => window.clearTimeout(copyTimer.current), []);

  async function share(next: ShareTTL) {
    setBusy(true);
    setError(null);
    try {
      const s = await createShare(videoId, next);
      onStatusChange(s);
      setTtl(next);
    } catch {
      setError("Couldn't update the link. Try again.");
    } finally {
      setBusy(false);
    }
  }

  async function stop() {
    setBusy(true);
    setError(null);
    try {
      const s = await stopShare(videoId);
      onStatusChange(s);
      setArmed(false);
    } catch {
      setError("Couldn't stop sharing. Try again.");
    } finally {
      setBusy(false);
    }
  }

  async function copy() {
    if (!status.url) return;
    try {
      await navigator.clipboard.writeText(absoluteUrl(status.url));
      setCopied(true);
      window.clearTimeout(copyTimer.current);
      copyTimer.current = window.setTimeout(() => setCopied(false), 2000);
    } catch {
      setError("Copy failed — select and copy the link by hand.");
    }
  }

  const liveLabel = () => {
    const d = daysUntil(status.expires_at);
    if (d === null) return "No expiry";
    if (d <= 0) return "Expires today";
    if (d === 1) return "1 day left";
    return `${d} days left`;
  };

  return (
    <span className="share-ctl" ref={wrapRef}>
      {children}

      {open && (
        <div
          className="sharepop"
          role="dialog"
          aria-label="Share this video"
          ref={popRef}
        >
          <div className="ph">
            <Icon name="share" size="15px" />
            <b>Share link</b>
            {status.shared && (
              <span className="live">
                <i />
                {liveLabel()}
              </span>
            )}
          </div>

          <div className="sharepop-body">
            {status.shared ? (
              <>
                <div className="sharepop-linkrow">
                  <div
                    className="sharepop-link"
                    title={absoluteUrl(status.url ?? "")}
                  >
                    {prettyUrl(status.url)}
                  </div>
                  <Button type="button" variant="primary" onClick={copy}>
                    <Icon name={copied ? "check" : "copy"} size="15px" />
                    {copied ? "Copied" : "Copy"}
                  </Button>
                </div>
                <div className="sharepop-seg">
                  {TTLS.map((o) => (
                    <button
                      key={o.id}
                      type="button"
                      disabled={busy}
                      className={o.id === ttl ? "sel" : undefined}
                      onClick={() => share(o.id)}
                    >
                      {o.label}
                    </button>
                  ))}
                </div>
                <p className="sharepop-help">
                  Changing this updates the same link.
                </p>
              </>
            ) : (
              <>
                <div className="sharepop-seg">
                  {TTLS.map((o) => (
                    <button
                      key={o.id}
                      type="button"
                      disabled={busy}
                      className={o.id === ttl ? "sel" : undefined}
                      onClick={() => setTtl(o.id)}
                    >
                      {o.label}
                    </button>
                  ))}
                </div>
                <div className="sharepop-note">
                  <Icon name="share" size="13px" />
                  <span>
                    Anyone with the link can watch — no account needed.
                  </span>
                </div>
                <Button
                  type="button"
                  variant="primary"
                  busy={busy}
                  onClick={() => share(ttl)}
                >
                  <Icon name="share" size="15px" /> Create link
                </Button>
              </>
            )}

            {error && <p className="sharepop-err">{error}</p>}

            {status.shared && (
              <div className="sharepop-foot">
                {armed ? (
                  <>
                    <span className="sharepop-arm">Turn off the link?</span>
                    <span className="grow" />
                    <Button
                      type="button"
                      variant="secondary"
                      small
                      onClick={() => setArmed(false)}
                    >
                      Cancel
                    </Button>
                    <Button
                      type="button"
                      variant="danger"
                      small
                      busy={busy}
                      onClick={stop}
                    >
                      Stop
                    </Button>
                  </>
                ) : (
                  <>
                    <span className="grow" />
                    <Button
                      type="button"
                      variant="dangerQuiet"
                      small
                      onClick={() => setArmed(true)}
                    >
                      <Icon name="trash" size="13px" /> Stop sharing
                    </Button>
                  </>
                )}
              </div>
            )}
          </div>
        </div>
      )}
    </span>
  );
}
