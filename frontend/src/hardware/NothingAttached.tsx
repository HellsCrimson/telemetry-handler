// A rig with no MOZA hardware at all — artboard 3c.
//
// This is not the "the wheel I know about went away" state; that one keeps the
// device in the rail and shows its last known values. This is a new rig, or
// someone on a different brand entirely, and for them the whole section is
// empty. It is the state a user is most likely to meet first and the one an
// empty tab bar explains worst.
//
// So it says three things a bare "no devices" cannot: that nothing is wrong,
// that the rest of the app works without any of this, and what would appear here
// if something were plugged in.

import { Panel } from "../design/Shell";

export default function NothingAttached({ onRescan, onPinPort }: { onRescan: () => void; onPinPort: () => void }) {
  return (
    <>
      <div className="base-strip offline" style={{ minHeight: 34 }}>
        <span className="who" style={{ gap: "var(--s-4)" }}>
          <span className="dot" />
        </span>
        <span className="channel">→ NOTHING TO WRITE TO · THE APP KEEPS LOOKING</span>
      </div>

      <div style={{ flex: 1, display: "flex", alignItems: "flex-start", justifyContent: "center", padding: "52px var(--s-7) 44px" }}>
        <div style={{ width: 840, maxWidth: "100%", display: "flex", flexDirection: "column", gap: "var(--s-8)" }}>
          <div
            style={{
              border: "1px dashed var(--line-strong)",
              borderRadius: "var(--radius)",
              background: "var(--bg-inset)",
              padding: "30px 30px 26px",
              display: "flex",
              gap: 26,
              alignItems: "flex-start",
            }}
          >
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ font: "600 10px var(--font-mono)", letterSpacing: "0.11em", color: "var(--ink-mid)", marginBottom: 10 }}>
                NO MOZA HARDWARE ON THIS RIG
              </div>
              <div style={{ font: "400 15px/1.5 var(--font-sans)", color: "var(--ink-hi)", marginBottom: 8 }}>
                This section configures MOZA wheelbases, rims and pedals. Nothing is attached, so there is nothing
                to configure yet.
              </div>
              <div style={{ font: "400 11.5px/1.6 var(--font-sans)", color: "var(--ink-mid)", maxWidth: 460 }}>
                Plug a base in and power it on — it appears in the rail within a couple of seconds and its tabs fill
                in. No setup, no pairing, no restart.
              </div>
              <div style={{ display: "flex", gap: "var(--s-5)", marginTop: 18 }}>
                <button className="btn-write" onClick={onRescan}>
                  RUN DETECTION NOW
                </button>
                <button className="btn" onClick={onPinPort} style={{ height: 28 }}>
                  Pin a port manually
                </button>
              </div>
            </div>

            <div
              style={{
                width: 290,
                flex: "none",
                background: "var(--bg-panel)",
                border: "1px solid var(--line)",
                borderRadius: "var(--radius-inner)",
                padding: "var(--s-6)",
              }}
            >
              <div style={{ font: "600 9.5px var(--font-mono)", letterSpacing: "var(--ls-label)", color: "var(--ink-lo)", marginBottom: 9 }}>
                WHAT DETECTION LOOKS FOR
              </div>
              <div style={{ display: "flex", flexDirection: "column", gap: 6, font: "400 10.5px var(--font-mono)", color: "var(--ink-mid)" }}>
                <Row label="SERIAL PORTS" value="/dev/ttyACM*" />
                <Row label="MOZA VENDOR" value="346E" />
                <Row label="PINNED PORT" value="none" />
              </div>
              {/* The single most common cause on this platform, said here rather
                  than left to a search. */}
              <div style={{ marginTop: 10, paddingTop: 10, borderTop: "1px solid var(--line)", font: "400 10px/1.5 var(--font-sans)", color: "var(--ink-lo)" }}>
                On Linux, a base that never appears is usually a udev permission on{" "}
                <span style={{ fontFamily: "var(--font-mono)", color: "var(--ink)" }}>/dev/ttyACM*</span>.
              </div>
            </div>
          </div>

          <div style={{ display: "grid", gridTemplateColumns: "repeat(2,minmax(0,1fr))", gap: "var(--s-7)" }}>
            <Panel label="DIFFERENT BRAND?">
              <div style={{ font: "400 11px/1.55 var(--font-sans)", color: "var(--ink-mid)" }}>
                Telemetry, recording and the race engineer all read from the game, not the wheel. The dashboard and
                the Strategy Planner work fully with any hardware — this section simply stays empty.
              </div>
            </Panel>
            <Panel label="WHAT THIS PAGE WILL SHOW">
              <div style={{ font: "400 11px/1.55 var(--font-sans)", color: "var(--ink-mid)" }}>
                Force feedback and steering range stored on the base, the rim's rev-light ramp and button colours,
                and per-device diagnostics — one rail entry per attached device.
              </div>
            </Panel>
          </div>
        </div>
      </div>
    </>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", gap: "var(--s-5)" }}>
      <span>{label}</span>
      <span style={{ color: "var(--ink)" }}>{value}</span>
    </div>
  );
}
