// The rim's LEDs — the right column of artboard 2a.
//
// These are a different capability from the wheelbase settings beside them: the
// base stores its own configuration and we read it back, whereas the LEDs are
// output this app pushes at telemetry rate and the wheel remembers nothing. So
// there is no drift marking here and no Apply — the values live in the app's own
// config and go out with the header's Apply/Save like every other app setting.
//
// The preview is the point of the panel. Rev-light colours are chosen by eye at
// a glance in peripheral vision, and a row of hex fields tells you nothing about
// whether the ramp reads correctly while a bar you can scrub does.

import { useState } from "react";
import { Panel } from "../design/Shell";
import { Field, FieldGroup, Slider, NumField } from "../design/Controls";
import { CurveEditor, evalCurve, presetCurve } from "../CurveEditor";

type RGB = number[];

interface MozaConfig {
  enabled: boolean;
  port: string;
  update_hz: number;
  rpm_brightness: number;
  rpm_colors: RGB[];
  button_colors: RGB[];
  button_mask: number;
  rpm_leds?: number;
  rpm_curve_points?: { x: number; y: number }[];
}

interface Props {
  moza: MozaConfig;
  patch: (mutate: (moza: MozaConfig) => void) => void;
  /** Rev-light count the driver resolved, used as the label for "auto". */
  detectedLeds: number;
  connected: boolean;
  onPreviewButtons: () => void;
  onTestLights: () => void;
  testingLights: boolean;
}

const PRESETS: [string, string][] = [
  ["linear", "Linear"],
  ["exponential", "Exponential"],
  ["logarithmic", "Logarithmic"],
  ["scurve", "S-curve"],
];

export function LivePreview({ moza, connected, onPreviewButtons, onTestLights, testingLights }: Props) {
  // A scrub bar rather than the live RPM: the wheel is usually being configured
  // with the game closed, and a preview that only works while driving is a
  // preview you cannot use when you need it.
  const [rpm, setRpm] = useState(0.91);

  const count = ledCount(moza);
  const fill = evalCurve(moza.rpm_curve_points ?? [], rpm);
  const lit = Math.round(fill * count);

  return (
    <Panel
      label="LIVE PREVIEW"
      meta={
        <span style={{ display: "flex", alignItems: "center", gap: "var(--s-3)" }}>
          <span
            style={{
              width: 6,
              height: 6,
              borderRadius: "50%",
              background: connected ? "var(--sem-good)" : "var(--ink-faint)",
            }}
          />
          {connected ? "WRITING TO WHEEL" : "NOT CONNECTED"}
        </span>
      }
    >
      <div style={{ background: "var(--bg-inset)", border: "1px solid var(--line-soft)", borderRadius: 2, padding: "14px 14px 12px" }}>
        <div style={{ display: "flex", gap: 3, height: 22, marginBottom: 10 }}>
          {Array.from({ length: count }, (_, i) => (
            <div
              key={i}
              style={{
                flex: 1,
                borderRadius: 1,
                background: i < lit ? rgb(moza.rpm_colors[i]) : "var(--bg-row-alt)",
                opacity: i < lit ? Math.max(0.25, moza.rpm_brightness / 15) : 1,
              }}
            />
          ))}
        </div>
        <div style={{ display: "flex", justifyContent: "space-between", font: "400 9.5px var(--font-mono)", color: "var(--ink-faint)" }}>
          <span>0%</span>
          <span style={{ color: "var(--ink-hi)" }}>
            RPM {Math.round(rpm * 100)}% · {lit} of {count} lit
          </span>
          <span>100%</span>
        </div>

        <div style={{ display: "flex", justifyContent: "center", gap: 10, marginTop: 14 }}>
          {moza.button_colors.slice(0, 6).map((c, i) => (
            <div
              key={i}
              style={{
                width: 26,
                height: 26,
                borderRadius: "50%",
                background: rgb(c),
                opacity: moza.button_mask & (1 << i) ? 0.9 : 0.15,
              }}
            />
          ))}
        </div>
      </div>

      <div style={{ marginTop: "var(--s-5)" }}>
        <Field label="RPM sweep" hint="Drag to see the ramp at any engine speed; the wheel itself is not driven by this.">
          <Slider value={Math.round(rpm * 100)} min={0} max={100} onChange={(v) => setRpm(v / 100)} ariaLabel="Preview RPM" />
          <button className="btn" onClick={onTestLights} disabled={!connected || testingLights} style={{ flex: "none" }}>
            {testingLights ? "Sweeping…" : "Run on wheel"}
          </button>
        </Field>
        <Field
          label="Button lights"
          hint={connected ? "Sends the colours below to the rim so you can see them in place." : "Connect the wheel to preview on the rim."}
          disabled={!connected}
        >
          <button className="btn" onClick={onPreviewButtons} disabled={!connected}>
            Preview on wheel
          </button>
        </Field>
      </div>
    </Panel>
  );
}

export function RpmRamp({ moza, patch, detectedLeds }: Props) {
  const count = ledCount(moza);
  const points = moza.rpm_curve_points ?? [];

  return (
    <Panel label="LEDS · RPM RAMP" meta={`${count} SEGMENT${count === 1 ? "" : "S"}`} flush>
      <div style={{ padding: "var(--s-6) var(--s-5)" }}>
        <div style={{ display: "flex", gap: 6, marginBottom: "var(--s-6)" }}>
          {Array.from({ length: count }, (_, i) => (
            <div key={i} style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column", alignItems: "center", gap: 5 }}>
              <input
                type="color"
                className="led-swatch"
                value={hex(moza.rpm_colors[i])}
                aria-label={`RPM colour ${i + 1}`}
                onChange={(e) => patch((m) => (m.rpm_colors[i] = fromHex(e.target.value)))}
              />
              {/* The RPM at which this segment lights, read off the curve — the
                  colours only make sense against where they actually appear. */}
              <span style={{ font: "400 9px var(--font-mono)", color: "var(--ink-faint)" }}>{litAt(points, i, count)}</span>
            </div>
          ))}
        </div>

        <FieldGroup>
          <Field label="Brightness" hint="How hard the rev lights are driven. Lower it if the rim washes out at night.">
            <Slider
              value={moza.rpm_brightness}
              min={0}
              max={15}
              onChange={(v) => patch((m) => (m.rpm_brightness = v))}
              ariaLabel="RPM brightness"
            />
            <NumField value={moza.rpm_brightness} min={0} max={15} onChange={(v) => patch((m) => (m.rpm_brightness = v))} ariaLabel="RPM brightness" />
          </Field>
          <Field
            label="Segments"
            hint={`0 follows the detected rim (${detectedLeds || "default"}). The rim model cannot be read over USB, so set it here if the lights look wrong.`}
          >
            <NumField
              value={moza.rpm_leds ?? 0}
              min={0}
              max={16}
              onChange={(v) => patch((m) => (m.rpm_leds = v))}
              ariaLabel="RPM LED count"
            />
          </Field>
          <Field label="Update rate" hint="How often the lights are rewritten while driving.">
            <NumField
              value={moza.update_hz}
              unit="Hz"
              min={1}
              max={120}
              onChange={(v) => patch((m) => (m.update_hz = v))}
              ariaLabel="Update rate"
            />
          </Field>
        </FieldGroup>

        <div style={{ marginTop: "var(--s-6)" }}>
          <div style={{ font: "var(--t-micro)", fontWeight: 600, letterSpacing: "var(--ls-label)", color: "var(--ink-lo)", marginBottom: "var(--s-4)" }}>
            RESPONSE CURVE
          </div>
          <CurveEditor points={points} colors={moza.rpm_colors} onChange={(pts) => patch((m) => (m.rpm_curve_points = pts))} />
          <div style={{ display: "flex", gap: "var(--s-4)", marginTop: "var(--s-4)" }}>
            {PRESETS.map(([id, label]) => (
              <button key={id} className="btn" onClick={() => patch((m) => (m.rpm_curve_points = presetCurve(id)))}>
                {label}
              </button>
            ))}
          </div>
          <p style={{ font: "400 9.5px/1.45 var(--font-sans)", color: "var(--ink-lo)", margin: "var(--s-4) 0 0" }}>
            Left is idle, right is max RPM. Bowing the curve below the diagonal keeps the green segments lit
            across a wider band and squeezes red into a small window near the top — useful when the engine
            rarely reaches its limit. Drag to bend, click to add a point, double-click to remove.
          </p>
        </div>
      </div>
    </Panel>
  );
}

export function ButtonColours({ moza, patch }: Props) {
  return (
    <Panel label="LEDS · BUTTON COLOURS" flush>
      <div style={{ padding: "var(--s-5)", display: "grid", gridTemplateColumns: "repeat(2,minmax(0,1fr))", gap: "var(--s-4) var(--s-6)" }}>
        {moza.button_colors.map((c, i) => {
          const on = !!(moza.button_mask & (1 << i));
          return (
            <div key={i} style={{ display: "flex", alignItems: "center", gap: 9, height: 26 }}>
              <input
                type="color"
                className={`led-swatch small${on ? "" : " unlit"}`}
                value={hex(c)}
                aria-label={`Button colour ${i + 1}`}
                onChange={(e) => patch((m) => (m.button_colors[i] = fromHex(e.target.value)))}
              />
              <span style={{ flex: 1, font: "var(--t-ui)", color: on ? "var(--ink)" : "var(--ink-faint)" }}>
                Button {String(i + 1).padStart(2, "0")}
              </span>
              {/* The mask decides which of these are lit at all, so it is edited
                  here beside the colours rather than as a number elsewhere. */}
              <button
                className="btn"
                aria-pressed={on}
                onClick={() => patch((m) => (m.button_mask = m.button_mask ^ (1 << i)))}
                style={{ font: "500 9px var(--font-mono)", letterSpacing: ".05em", color: on ? "var(--ink-hi)" : "var(--ink-faint)" }}
              >
                {on ? "LIT" : "OFF"}
              </button>
            </div>
          );
        })}
      </div>
    </Panel>
  );
}

function ledCount(moza: MozaConfig): number {
  const n = moza.rpm_leds || moza.rpm_colors.length;
  return Math.max(1, Math.min(moza.rpm_colors.length, n));
}

/** litAt reports the RPM percentage at which segment i first lights, by walking
 *  the curve rather than assuming it is linear. */
function litAt(points: { x: number; y: number }[], index: number, count: number): string {
  const target = (index + 1) / count;
  for (let x = 0; x <= 100; x++) {
    if (evalCurve(points, x / 100) >= target - 1e-6) return `${x}%`;
  }
  return "—";
}

const clamp255 = (v: number) => (v < 0 ? 0 : v > 255 ? 255 : Math.round(v));
const rgb = (c: RGB | undefined) => `rgb(${(c ?? [0, 0, 0]).map(clamp255).join(",")})`;
const hex = (c: RGB | undefined) =>
  "#" + (c ?? [0, 0, 0]).map((v) => clamp255(v).toString(16).padStart(2, "0")).join("");
const fromHex = (h: string): RGB => [
  parseInt(h.slice(1, 3), 16),
  parseInt(h.slice(3, 5), 16),
  parseInt(h.slice(5, 7), 16),
];
