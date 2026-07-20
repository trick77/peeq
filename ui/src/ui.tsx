import type { ButtonHTMLAttributes, CSSProperties, ReactNode } from "react";
import { Icon } from "./icons";

/**
 * ui — shared design-system primitives, ported from ../music's ui.tsx.
 *
 * peeq already had music's tokens; what it lacked was anything enforcing them,
 * so four parallel button systems (.btn / .abtn / .tokenbtn / .iconbtn) and ten
 * type sizes grew in index.css. The whole app styles through these instead:
 * one type scale, one 40px control, one button set.
 *
 * DELIBERATELY NOT PORTED FROM MUSIC — peeq keeps its own colours:
 *   - `primary` fills with --color-accent, not music's --color-accent-fill.
 *   - `tinted` is peeq's .abtn.accent treatment (clay text on a 12% clay wash),
 *     used for Re-download on a failed card. music has no equivalent.
 * Only geometry and type move onto the system; the palette stays as it was.
 */

/** Type scale — six steps, nothing in between. Serif is for content headings only. */
export const t = {
  display: { fontFamily: "var(--font-serif)", fontWeight: 500, fontSize: "var(--text-display)" },
  title: { fontFamily: "var(--font-serif)", fontWeight: 500, fontSize: "var(--text-title)" },
  body: { fontSize: "var(--text-body)" },
  ui: { fontSize: "var(--text-ui)" },
  label: { fontSize: "var(--text-label)", color: "var(--color-muted)", fontWeight: 500 },
  micro: {
    fontSize: "var(--text-micro)",
    textTransform: "uppercase",
    letterSpacing: "0.06em",
    color: "var(--color-muted)",
  },
} satisfies Record<string, CSSProperties>;

/** Field label: 13px muted, 6px above its control. Replaces the 11px uppercase .lab. */
export const fieldLabel: CSSProperties = { display: "block", ...t.label, marginBottom: 6 };

/**
 * The 40px form control. Prefer the `ui-control` class (it carries the focus ring);
 * `controlStyle` is the same look as an inline object for composition.
 *
 * Font size comes from --text-input, which is 15px on a fine pointer and 16px on a
 * coarse one. That bump is not cosmetic: iOS Safari zooms the page in whenever a
 * focused field renders under 16px and never zooms back out. Do not replace it with
 * --text-ui, and do not set an inline fontSize here — an inline value out-specifies
 * the media query that does the bumping.
 */
export const controlClass = "ui-control";
export const controlStyle: CSSProperties = {
  width: "100%",
  minHeight: 40,
  padding: "0 12px",
  background: "var(--color-panel)",
  border: "1px solid var(--color-border)",
  borderRadius: "var(--radius-ui)",
  color: "var(--color-ink)",
  fontFamily: "var(--font-sans)",
  fontSize: "var(--text-input)",
  outline: "none",
};

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger" | "tinted";

const variantStyle: Record<ButtonVariant, CSSProperties> = {
  // peeq's existing primary colour — deliberately not music's --color-accent-fill.
  primary: {
    background: "var(--color-accent)",
    color: "var(--color-bg)",
    fontWeight: 600,
    border: "1px solid transparent",
    boxShadow: "0 8px 24px -10px var(--glow-accent)",
  },
  secondary: {
    background: "var(--color-active)",
    color: "var(--color-ink-dim)",
    fontWeight: 500,
    border: "1px solid var(--color-border)",
  },
  ghost: {
    background: "transparent",
    color: "var(--color-accent-strong)",
    fontWeight: 500,
    border: "1px solid transparent",
  },
  danger: {
    background: "transparent",
    color: "var(--color-danger)",
    fontWeight: 600,
    border: "1px solid color-mix(in srgb, var(--color-danger) 35%, transparent)",
  },
  // peeq-only: the old .abtn.accent, kept verbatim.
  tinted: {
    background: "color-mix(in srgb, var(--color-accent-strong) 12%, transparent)",
    color: "var(--color-accent-strong)",
    fontWeight: 500,
    border: "1px solid var(--color-accent-dim)",
  },
};

/** Raw button style object (height 40 / radius 10 / 15px), for call sites needing a style. */
export function buttonStyle(
  variant: ButtonVariant = "primary",
  opts?: { small?: boolean; icon?: boolean },
): CSSProperties {
  const small = opts?.small ?? false;
  const icon = opts?.icon ?? false;
  return {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    gap: 7,
    minHeight: small || icon ? 32 : 40,
    ...(icon ? { width: 32, padding: 0, flex: "none" } : { padding: small ? "0 12px" : "0 16px" }),
    borderRadius: "var(--radius-ui)",
    fontFamily: "var(--font-sans)",
    fontSize: small ? "var(--text-label)" : "var(--text-ui)",
    cursor: "pointer",
    ...variantStyle[variant],
  };
}

/** Spinner — every async wait spins. Never an ellipsis in the label. */
export function Spinner({ size = "15px" }: { size?: string }) {
  return (
    <span className="ui-spin">
      <Icon name="spinner" size={size} />
    </span>
  );
}

type ButtonProps = {
  variant?: ButtonVariant;
  small?: boolean;
  /** Square 32px icon-only button. Pass `aria-label` — there is no visible text. */
  icon?: boolean;
  /** Busy shows a leading spinner and disables the button. The label stays, without an ellipsis. */
  busy?: boolean;
  children?: ReactNode;
} & Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children">;

/**
 * Button — the one button primitive. Replaces .btn, .abtn and .tokenbtn.
 * Disabled/busy convention: opacity 0.6, spinner when busy.
 */
export function Button({
  variant = "primary",
  small,
  icon,
  busy,
  disabled,
  children,
  style,
  ...rest
}: ButtonProps) {
  const off = disabled || busy;
  return (
    <button
      {...rest}
      disabled={off}
      style={{
        ...buttonStyle(variant, { small, icon }),
        ...(off ? { opacity: 0.6, cursor: "default" } : null),
        ...style,
      }}
    >
      {busy && <Spinner size={small || icon ? "13px" : "15px"} />}
      {children}
    </button>
  );
}
