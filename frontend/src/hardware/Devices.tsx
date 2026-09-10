// Devices & ports — artboard 3b.
//
// The page you open when something is WRONG: nothing detected, the wrong port
// pinned, the app talking to a serial adapter instead of a base. That is a
// different errand from tuning force feedback, which is why it is rig-level and
// sits below the hairline in the rail rather than being a tab on a device.
//
// Everything here writes the APP's config, never the base — hence the badge on
// the panel head and the absence of the base channel above it.

import { Panel, Empty } from "../design/Shell";
import { Toggle } from "../design/Controls";
import type { Wheelbase } from "../moza/useWheelbase";
import type { Device, MozaStatus } from "./HardwareApp";

interface Props {
  status: MozaStatus;
  devices: Device[];
  moza: any;
  patch: (mutate: (moza: any) => void) => void;
  wb: Wheelbase;
}

export default function Devices({ status, devices, moza, patch }: Props) {
  const pinned: string = moza.port ?? "";
  const pin = (port: string) => patch((m) => (m.port = port));

  // The failure this page exists for: a port is pinned, but the base was found
  // somewhere else. Silently using the wrong device looks identical to broken
  // hardware, so it is called out with the fix attached.
  const pinnedIsWrong = pinned !== "" && devices.length > 0 && !devices.some((d) => d.port === pinned);
  const suggestion = devices[0]?.port;

  return (
    <>
      {pinnedIsWrong && (
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: "var(--s-5)",
            height: 38,
            padding: "0 var(--s-7)",
            background: "color-mix(in oklch, var(--sem-warn) 10%, transparent)",
            borderBottom: "1px solid color-mix(in oklch, var(--sem-warn) 28%, transparent)",
          }}
        >
          <span style={{ width: 8, height: 8, background: "var(--sem-warn)", flex: "none" }} />
          <span style={{ font: "400 11.5px var(--font-sans)", color: "var(--ink-hi)" }}>
            {pinned} is pinned but nothing MOZA answered there.
            {suggestion ? ` A device was found on ${suggestion}.` : " No MOZA device was found at all."}
          </span>
          <span style={{ flex: 1 }} />
          {suggestion && (
            <button
              className="btn-write"
              style={{ height: 24, color: "var(--sem-warn)", borderColor: "color-mix(in oklch, var(--sem-warn) 45%, transparent)" }}
              onClick={() => pin(suggestion)}
            >
              REPIN TO {suggestion.replace(/^\/dev\//, "")}
            </button>
          )}
          <button className="btn" onClick={() => pin("")}>
            Unpin
          </button>
        </div>
      )}

      <div className="set-grid" style={{ ["--set-cols" as string]: 2 }}>
        <div className="set-col">
          <Panel label="ATTACHED" head={<span className="target-badge">APP CONFIG</span>} flush>
            {devices.length === 0 ? (
              <div style={{ padding: "var(--s-7)" }}>
                {/* Not an error state. The base is switched off between sessions
                    far more often than it is faulty. */}
                <Empty title="NOTHING ATTACHED">
                  No MOZA device is on the USB bus. The base has to be powered up to appear here — the app keeps
                  looking, so this fills in on its own once it is.
                </Empty>
              </div>
            ) : (
              <>
                <div
                  style={{
                    display: "grid",
                    gridTemplateColumns: "minmax(0,1fr) 130px 62px 110px",
                    gap: "0 var(--s-5)",
                    padding: "0 var(--s-6)",
                    height: 24,
                    alignItems: "center",
                    borderBottom: "1px solid var(--line)",
                    font: "var(--t-micro)",
                    fontWeight: 500,
                    letterSpacing: "0.07em",
                    color: "var(--ink-lo)",
                  }}
                >
                  <span>DEVICE</span>
                  <span>PORT</span>
                  <span style={{ textAlign: "center" }}>PIN</span>
                  <span>LINK</span>
                </div>
                {devices.map((d) => {
                  const inUse = status.connected && status.port === d.port;
                  return (
                    <div
                      key={d.port}
                      style={{
                        display: "grid",
                        gridTemplateColumns: "minmax(0,1fr) 130px 62px 110px",
                        gap: "0 var(--s-5)",
                        padding: "0 var(--s-6)",
                        height: 34,
                        alignItems: "center",
                        borderBottom: "1px solid var(--line-soft)",
                      }}
                    >
                      <span style={{ display: "flex", alignItems: "center", gap: "var(--s-4)", minWidth: 0 }}>
                        <span
                          style={{
                            width: 6,
                            height: 6,
                            borderRadius: "50%",
                            flex: "none",
                            background: inUse ? "var(--sem-good)" : "var(--ink-faint)",
                          }}
                        />
                        <span style={{ font: "var(--t-ui)", color: "var(--ink)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                          {d.model}
                        </span>
                      </span>
                      <span style={{ font: "400 11px var(--font-mono)", color: "var(--ink)" }}>{d.port}</span>
                      <span style={{ textAlign: "center" }}>
                        <button className="btn" onClick={() => pin(pinned === d.port ? "" : d.port)} style={{ height: 20, padding: "0 var(--s-4)" }}>
                          {pinned === d.port ? "PINNED" : "PIN"}
                        </button>
                      </span>
                      <span style={{ font: "var(--t-micro)", fontWeight: 500, letterSpacing: "0.05em", color: inUse ? "var(--sem-good)" : "var(--ink-lo)" }}>
                        {inUse ? "IN USE" : "IDLE"}
                      </span>
                    </div>
                  );
                })}
              </>
            )}

            <div style={{ display: "flex", alignItems: "center", gap: "var(--s-5)", height: 34, padding: "0 var(--s-6)", borderTop: "1px solid var(--line)" }}>
              <Toggle value={!!moza.enabled} onChange={(on) => patch((m) => (m.enabled = on))} ariaLabel="LED output" />
              <span style={{ font: "var(--t-ui)", color: "var(--ink)" }}>LED output enabled</span>
              <span style={{ font: "400 10.5px var(--font-sans)", color: "var(--ink-lo)" }}>
                app drives the rim LEDs while a game is running
              </span>
            </div>
          </Panel>
        </div>

        <div className="set-col">
          <Panel label="SERIAL PORT" head={<span className="target-badge">APP CONFIG</span>}>
            <div style={{ display: "flex", flexDirection: "column", gap: "var(--s-5)" }}>
              <input
                className="textfield mono"
                autoComplete="off"
                placeholder="auto-detect"
                value={pinned}
                onChange={(e) => pin(e.target.value)}
                aria-label="Serial port"
              />
              <span style={{ font: "400 10.5px/1.5 var(--font-sans)", color: "var(--ink-lo)" }}>
                Blank follows whatever detection finds, which is right on almost every rig. Pin one only when you
                have more than one serial device and the app keeps choosing the wrong one — a stale pin is the most
                common reason a working base looks dead.
              </span>
            </div>
          </Panel>
        </div>
      </div>
    </>
  );
}
