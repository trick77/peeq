import { useCallback } from "react";
import { Icon, type IconName } from "../icons";
import { useTransient } from "../hooks/useTransient";

// StageToast is the transient notice over a video stage. It began as the
// SponsorBlock skip message and now carries action failures too, hence the
// icon and tone: tone drives the styling, so a failure stays red even if it
// later picks a more specific icon, and an advisory that happens to use the
// warning glyph does not turn red by accident.
export type StageToast = {
  text: string;
  icon: IconName;
  tone: "info" | "warn";
};

// How long a stage toast stays up.
export const STAGE_TOAST_MS = 2600;

// useStageToast is the one toast a stage holds, shared by the Player and the
// share page. A later toast replaces an earlier one, so the clock is always
// reset rather than stacked — two overlapping notices would otherwise leave
// the second one dismissed early by the first one's timeout.
export function useStageToast(): {
  toast: StageToast | null;
  show: (text: string, icon?: IconName, tone?: StageToast["tone"]) => void;
  clear: () => void;
} {
  const t = useTransient<StageToast>(STAGE_TOAST_MS);
  const show = useCallback(
    (
      text: string,
      icon: IconName = "skipForward",
      tone: StageToast["tone"] = "info",
    ) => t.show({ text, icon, tone }),
    [t.show],
  );
  return { toast: t.value, show, clear: t.clear };
}

// StageToastView draws the toast. Always rendered (the class carries the
// visibility), so the show/hide is a transition and not a mount.
export function StageToastView({ toast }: { toast: StageToast | null }) {
  return (
    <div
      className={`stage-toast${toast ? " show" : ""}${
        toast?.tone === "warn" ? " warn" : ""
      }`}
      role="status"
    >
      <Icon name={toast?.icon ?? "skipForward"} size="15px" />
      {toast?.text}
    </div>
  );
}
