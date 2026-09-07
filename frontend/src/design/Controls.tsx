// Form controls, as components rather than markup conventions.
//
// The MOZA wheelbase page and both Settings tabs need the identical set, and the
// drift pattern (a value differing from the saved profile) has to behave the
// same everywhere or it stops being a signal. Both are reasons this lives here
// and not in the screens.

import type { ReactNode } from "react";

/** A control's relationship to the saved profile. `drifted` paints the amber
 *  spine, border and delta; `disabled` flattens it and expects a reason. */
interface FieldState {
  drifted?: boolean;
  disabled?: boolean;
}

interface FieldProps extends FieldState {
  label: string;
  /** hint is the one-line explanation. Visible, because a setting that needs a
   *  tooltip to be understood is a setting that needs better words. */
  hint?: string;
  /** delta is the signed difference from the saved value, e.g. "+6". */
  delta?: string;
  /** labelWidth overrides the label column for a denser or wider panel. */
  labelWidth?: number;
  children: ReactNode;
}

export function Field({ label, hint, delta, drifted, disabled, labelWidth, children }: FieldProps) {
  const cls = ["field", drifted ? "drifted" : "", disabled ? "disabled" : ""].filter(Boolean).join(" ");
  return (
    <div className={cls} style={labelWidth ? ({ ["--field-label" as string]: `${labelWidth}px` } as React.CSSProperties) : undefined}>
      <div>
        <label className="field-label">{label}</label>
        {hint && <span className="field-hint">{hint}</span>}
      </div>
      <div className="field-control">{children}</div>
      <div className="field-delta">{delta ?? ""}</div>
    </div>
  );
}

export function FieldGroup({ label, children }: { label?: string; children: ReactNode }) {
  return (
    <div className="field-group">
      {label && <div className="field-group-head">{label}</div>}
      {children}
    </div>
  );
}

interface SliderProps extends FieldState {
  value: number;
  min?: number;
  max?: number;
  step?: number;
  onChange: (value: number) => void;
  /** saved is the profile value, drawn as a ghost mark. Without it "drifted"
   *  says something changed but not from what. */
  saved?: number;
  ariaLabel?: string;
}

export function Slider({ value, min = 0, max = 100, step = 1, onChange, saved, disabled, ariaLabel }: SliderProps) {
  const span = max - min || 1;
  const pct = ((value - min) / span) * 100;
  const savedPct = saved === undefined ? null : ((saved - min) / span) * 100;
  return (
    // webkit-fill paints the filled portion into the track via a gradient;
    // Firefox uses its native ::-moz-range-progress and ignores it.
    <div
      className={`slider webkit-fill${disabled ? " disabled" : ""}`}
      style={{ ["--fill" as string]: `${pct}%` } as React.CSSProperties}
    >
      {savedPct !== null && Math.abs(savedPct - pct) > 0.5 && (
        <span className="ghost" style={{ left: `calc(${savedPct}% - 0.5px)` }} />
      )}
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        disabled={disabled}
        aria-label={ariaLabel}
        onChange={(e) => onChange(Number(e.target.value))}
      />
    </div>
  );
}

interface NumFieldProps extends FieldState {
  value: number;
  unit?: string;
  min?: number;
  max?: number;
  step?: number;
  onChange: (value: number) => void;
  ariaLabel?: string;
}

/** Always paired with a Slider — the slider is for feel, the field is for a
 *  number someone gave you. Never the only way to set a value. */
export function NumField({ value, unit, min, max, step, onChange, drifted, disabled, ariaLabel }: NumFieldProps) {
  const cls = ["numfield", drifted ? "drifted" : "", disabled ? "disabled" : ""].filter(Boolean).join(" ");
  return (
    <div className={cls}>
      <input
        type="number"
        value={Number.isFinite(value) ? value : ""}
        min={min}
        max={max}
        step={step}
        disabled={disabled}
        aria-label={ariaLabel}
        onChange={(e) => {
          const next = Number(e.target.value);
          if (Number.isFinite(next)) onChange(next);
        }}
      />
      {unit && <span className="unit">{unit}</span>}
    </div>
  );
}

/** SliderField is the pairing used for every tunable value: slider, number
 *  field, and the delta from the saved profile derived once rather than at each
 *  call site. */
interface SliderFieldProps {
  label: string;
  hint?: string;
  value: number;
  saved?: number;
  unit?: string;
  min?: number;
  max?: number;
  step?: number;
  disabled?: boolean;
  labelWidth?: number;
  onChange: (value: number) => void;
}

export function SliderField({
  label,
  hint,
  value,
  saved,
  unit,
  min = 0,
  max = 100,
  step = 1,
  disabled,
  labelWidth,
  onChange,
}: SliderFieldProps) {
  const drifted = saved !== undefined && saved !== value;
  const delta = drifted ? signed(value - (saved as number)) : undefined;
  return (
    <Field label={label} hint={hint} delta={delta} drifted={drifted} disabled={disabled} labelWidth={labelWidth}>
      <Slider
        value={value}
        min={min}
        max={max}
        step={step}
        saved={saved}
        disabled={disabled}
        onChange={onChange}
        ariaLabel={label}
      />
      <NumField
        value={value}
        unit={unit}
        min={min}
        max={max}
        step={step}
        drifted={drifted}
        disabled={disabled}
        onChange={onChange}
        ariaLabel={label}
      />
    </Field>
  );
}

export interface Option {
  value: string;
  label: string;
  /** disabled marks an option that exists but cannot be chosen — "not
   *  installed" rather than absent, so the user knows it is a possibility. */
  disabled?: boolean;
}

interface SelectProps extends FieldState {
  value: string;
  options: readonly Option[];
  onChange: (value: string) => void;
  ariaLabel?: string;
}

export function Select({ value, options, onChange, disabled, ariaLabel }: SelectProps) {
  return (
    <div className={`select${disabled ? " disabled" : ""}`}>
      <select value={value} disabled={disabled} aria-label={ariaLabel} onChange={(e) => onChange(e.target.value)}>
        {options.map((o) => (
          <option key={o.value} value={o.value} disabled={o.disabled}>
            {o.label}
          </option>
        ))}
      </select>
    </div>
  );
}

interface SegmentedProps {
  value: string;
  options: readonly Option[];
  onChange: (value: string) => void;
  disabled?: boolean;
  ariaLabel?: string;
}

/** For a small closed set where seeing every option matters — a mode, not a
 *  list. Past four or five options, use a Select. */
export function Segmented({ value, options, onChange, disabled, ariaLabel }: SegmentedProps) {
  return (
    <div className="segmented" role="group" aria-label={ariaLabel}>
      {options.map((o) => (
        <button
          key={o.value}
          aria-pressed={value === o.value}
          disabled={disabled || o.disabled}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

interface ToggleProps {
  value: boolean;
  onChange: (value: boolean) => void;
  /** text is the state read aloud beside the switch ("ON"/"OFF" by default) —
   *  a switch alone is ambiguous about which way is on. */
  text?: string;
  disabled?: boolean;
  ariaLabel?: string;
}

export function Toggle({ value, onChange, text, disabled, ariaLabel }: ToggleProps) {
  return (
    <button
      type="button"
      className="toggle"
      aria-pressed={value}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={() => onChange(!value)}
    >
      <span className="track">
        <span className="knob" />
      </span>
      <span className="toggle-text">{text ?? (value ? "ON" : "OFF")}</span>
    </button>
  );
}

/** The semantic ramp, which is the whole palette a swatch may pick from. An
 *  arbitrary picker would let the LEDs disagree with every other colour in the
 *  app, and the ramp is what the driver has already learned. */
export const SWATCH_COLORS = [
  "var(--sem-cold)",
  "var(--sem-cool)",
  "var(--sem-good)",
  "var(--sem-warn)",
  "var(--sem-crit)",
  "var(--sem-best)",
] as const;

interface SwatchesProps {
  /** value is an index into SWATCH_COLORS, or null for "no LED". */
  value: number | null;
  onChange: (value: number | null) => void;
  /** allowNone adds the dashed "no LED" slot. */
  allowNone?: boolean;
  disabled?: boolean;
  ariaLabel?: string;
}

export function Swatches({ value, onChange, allowNone = true, disabled, ariaLabel }: SwatchesProps) {
  return (
    <div className="swatches" role="group" aria-label={ariaLabel}>
      {SWATCH_COLORS.map((color, i) => (
        <button
          key={color}
          type="button"
          className="swatch"
          style={{ background: color }}
          aria-pressed={value === i}
          aria-label={`Colour ${i + 1}`}
          disabled={disabled}
          onClick={() => onChange(i)}
        />
      ))}
      {allowNone && (
        <button
          type="button"
          className="swatch none"
          aria-pressed={value === null}
          aria-label="No LED"
          disabled={disabled}
          onClick={() => onChange(null)}
        />
      )}
    </div>
  );
}

/** signed renders a delta the way the gutter wants it: always with a sign, and
 *  without a trailing ".0" on whole numbers. */
function signed(n: number): string {
  const rounded = Math.abs(n) < 10 ? Math.round(n * 100) / 100 : Math.round(n);
  return `${rounded > 0 ? "+" : ""}${rounded}`;
}
