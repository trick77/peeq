import { useEffect, useId, useRef, type ReactNode } from "react";
import { Button } from "../ui";

// ConfirmDialog — a modal confirmation, the loom ModalShell/DeleteArtifactModal
// pattern in peeq's tokens, replacing window.confirm for destructive actions so
// the warning wears the app's own type and buttons instead of a bare browser
// alert. A full-screen scrim (click-away cancels) centres a small panel;
// Escape cancels; focus lands on Cancel so the safe choice is the default and
// an accidental Enter never deletes.
//
// It renders nothing when open is false, so a caller can keep it mounted and
// just flip the flag.
export function ConfirmDialog({
  open,
  title,
  children,
  confirmLabel = "Delete",
  busy = false,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  children: ReactNode;
  confirmLabel?: string;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const titleId = useId();

  // Keep the latest onCancel in a ref so the Escape listener subscribes once
  // per open, not on every parent re-render (callers pass a fresh inline arrow
  // each time — a new identity that would otherwise re-run this effect).
  const onCancelRef = useRef(onCancel);
  onCancelRef.current = onCancel;

  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onCancelRef.current();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  if (!open) return null;

  return (
    <div
      className="modal-overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onCancel();
      }}
    >
      <div
        className="modal-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
      >
        <h2 id={titleId} className="modal-title">
          {title}
        </h2>
        <div className="modal-body">{children}</div>
        <div className="modal-actions">
          <Button
            type="button"
            variant="secondary"
            autoFocus
            onClick={onCancel}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            busy={busy}
            onClick={onConfirm}
          >
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  );
}
