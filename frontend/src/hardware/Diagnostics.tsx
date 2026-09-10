// The base's own health — temperatures, link, identity.
//
// A device tab rather than part of the settings, because none of it is
// configuration: it is how the hardware IS, not what it has been told to do.
// It is also the only place in the section where the semantic ramp appears on
// something other than a state dot, which is why the thresholds are stated on
// the panel rather than left for the colour to imply.

import { Panel, Stat } from "../design/Shell";
import type { Wheelbase } from "../moza/useWheelbase";
import type { MozaStatus } from "./HardwareApp";

const TEMP_LABELS: Record<string, string> = {
  mcu_temp: "MCU",
  mosfet_temp: "MOSFET",
  motor_temp: "MOTOR",
};

/** The base derates its own output above this, so it is worth saying rather than
 *  just colouring the number red. */
const WARN_C = 55;
const CRIT_C = 70;
const SCALE_C = 90;

export default function Diagnostics({ status, wb }: { status: MozaStatus; wb: Wheelbase }) {
  const snap = wb.snap;
  const temps = Object.entries(snap?.temps ?? {});
  const hottest = temps.reduce((m, [, v]) => Math.max(m, v), 0);

  return (
    <div className="set-grid" style={{ ["--set-cols" as string]: 2 }}>
      <div className="set-col">
        <Panel label="TEMPERATURES" meta={`AMBER ${WARN_C}° · RED ${CRIT_C}°`} flush>
          {temps.length === 0 ? (
            <div style={{ padding: "var(--s-6)", font: "400 10.5px var(--font-sans)", color: "var(--ink-lo)" }}>
              The base did not answer for temperatures. Its status group is best-effort — the settings above are
              unaffected.
            </div>
          ) : (
            <div style={{ padding: "var(--s-6)" }}>
              {temps.map(([key, value]) => (
                <div
                  key={key}
                  style={{
                    display: "grid",
                    gridTemplateColumns: "74px minmax(0,1fr) 56px",
                    gap: "var(--s-5)",
                    alignItems: "center",
                    height: 32,
                    borderBottom: "1px solid var(--line-soft)",
                  }}
                >
                  <span style={{ font: "var(--t-micro)", fontWeight: 600, letterSpacing: "var(--ls-label)", color: "var(--ink-lo)" }}>
                    {TEMP_LABELS[key] ?? key.toUpperCase()}
                  </span>
                  <span style={{ position: "relative", height: 8, background: "var(--bg-inset)", border: "1px solid var(--line-soft)" }}>
                    <span
                      style={{
                        position: "absolute",
                        left: 0,
                        top: 0,
                        bottom: 0,
                        width: `${Math.min(100, (value / SCALE_C) * 100)}%`,
                        background: tone(value),
                      }}
                    />
                    {/* The two thresholds, marked on the bar so a reading is
                        placed against them rather than only coloured by them. */}
                    <span style={{ position: "absolute", left: `${(WARN_C / SCALE_C) * 100}%`, top: -2, bottom: -2, width: 1, background: "var(--line-strong)" }} />
                    <span style={{ position: "absolute", left: `${(CRIT_C / SCALE_C) * 100}%`, top: -2, bottom: -2, width: 1, background: "var(--ink-faint)" }} />
                  </span>
                  <span style={{ textAlign: "right", font: "500 15px var(--font-mono)", color: tone(value) }}>
                    {value.toFixed(0)}°
                  </span>
                </div>
              ))}

              {hottest >= CRIT_C && (
                <div style={{ display: "flex", gap: 9, marginTop: "var(--s-5)", alignItems: "flex-start" }}>
                  <span style={{ width: 8, height: 8, background: "var(--sem-crit)", flex: "none", marginTop: 3 }} />
                  <span style={{ font: "400 10.5px/1.5 var(--font-sans)", color: "var(--ink)" }}>
                    Above {CRIT_C}° the base derates its own output — expect less torque until it drops. Nothing to
                    fix; give it a few minutes with the wheel off.
                  </span>
                </div>
              )}
            </div>
          )}
        </Panel>
      </div>

      <div className="set-col">
        <Panel label="LINK & IDENTITY" flush>
          <div className="cell-grid" style={{ gridTemplateColumns: "repeat(2,minmax(0,1fr))", border: 0, borderRadius: 0 }}>
            <Stat label="LINK STATE" value={status.connected ? "OK" : "OFFLINE"} tone={status.connected ? "good" : ""} />
            <Stat label="PROTOCOL" value={status.protocol ? status.protocol.toUpperCase() : "—"} />
            <Stat label="RIM" value={status.wheel || "—"} />
            <Stat label="REV LIGHTS" value={status.connected ? status.rpm_leds : "—"} />
            <Stat label="SERIAL" value={status.serial || "—"} />
            <Stat label="PORT" value={status.port || "—"} />
          </div>
          {/* The base's raw state word. Not decoded yet, and a non-zero error is
              worth surfacing before we can name it rather than hiding until we
              can. */}
          {(snap?.has_state || snap?.has_error) && (
            <div className="cell-grid" style={{ gridTemplateColumns: "repeat(2,minmax(0,1fr))", border: 0, borderRadius: 0, borderTop: "1px solid var(--line)" }}>
              {snap?.has_state && <Stat label="STATE" value={snap.state} note="raw" />}
              {snap?.has_error && (
                <Stat label="ERROR" value={snap.error} note={snap.error === 0 ? "none" : "raw"} tone={snap.error === 0 ? "" : "crit"} />
              )}
            </div>
          )}
        </Panel>
      </div>
    </div>
  );
}

const tone = (c: number) => (c >= CRIT_C ? "var(--sem-crit)" : c >= WARN_C ? "var(--sem-warn)" : "var(--sem-good)");
