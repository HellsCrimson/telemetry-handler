// Hardware — the third top-level interface, beside the dashboard and the pit
// wall. Artboard set 3.
//
// It is a mode rather than a dashboard tab because the three are separated by
// WHEN you use them, not by subject. The other two are live telemetry read while
// a car is on track; this is the garage, set up once with the game closed.
//
// NAVIGATION: rail = presence, tabs = capability, scoped to the selected device.
// The rail scales to any number of devices without inventing tabs, and a
// device-scoped "LEDs" is unambiguous the moment both a base and a rim have
// them — which is exactly where a flat capability bar breaks.

import { useMemo, useState } from "react";
import { AppHeader, TabBar, Empty, Panel, type Mode, type TabDef } from "../design/Shell";
import type { Wheelbase } from "../moza/useWheelbase";
import BasePanel, { TAB_GROUPS } from "../moza/BasePanel";
import { LivePreview, RpmRamp, ButtonColours } from "../moza/LedPanels";
import DeviceRail, { type RailDevice } from "./DeviceRail";
import BaseChannel from "./BaseChannel";
import Devices from "./Devices";
import NothingAttached from "./NothingAttached";
import Diagnostics from "./Diagnostics";

export interface MozaStatus {
  enabled: boolean;
  connected: boolean;
  port: string;
  model: string;
  serial: string;
  rpm_leds: number;
  wheel: string;
  protocol: string;
}

export interface Device {
  port: string;
  model: string;
}

interface Props {
  config: any;
  patch: (mutate: (config: any) => void) => void;
  onSave: () => void;
  onDiscard: () => void;
  /** Number of app-config values differing from what is on disk. */
  appChanges: number;
  status: MozaStatus;
  devices: Device[];
  onRescan: () => void;
  onPreviewButtons: () => void;
  onTestLights: () => void;
  testingLights: boolean;
  /** Owned by App so staged changes survive leaving the mode. */
  wb: Wheelbase;
  onMode: (mode: Mode) => void;
}

/** The tabs each device offers. Scoped per device, which is the whole point of
 *  the rail: the rim's LED tab and the base's indicator tab never collide. */
const BASE_TABS: TabDef[] = [
  { id: "base", label: "Base Settings" },
  { id: "limits", label: "Wheel & Limits" },
  { id: "indicators", label: "Indicators" },
  { id: "diag", label: "Diagnostics" },
];

const RIM_TABS: TabDef[] = [
  { id: "rev", label: "Rev Lights" },
  { id: "buttons", label: "Button LEDs" },
];

export default function HardwareApp(props: Props) {
  const { config, patch, status, wb } = props;
  const [selected, setSelected] = useState("wheelbase");
  // Each device reopens on the tab you left it on, so switching to check a
  // temperature does not cost you your place.
  const [tabs, setTabs] = useState<Record<string, string>>({ wheelbase: "base", rim: "rev" });

  const rail = useMemo<RailDevice[]>(() => {
    const out: RailDevice[] = [];
    if (status.connected || props.devices.length > 0 || status.model) {
      out.push({
        id: "wheelbase",
        name: status.model || "Wheelbase",
        meta: status.connected ? `${status.protocol || "serial"} · ${status.port}` : "not detected",
        online: status.connected,
        staged: wb.changed.length,
      });
    }
    // The rim is its own rail row rather than a drill-down under the base, so
    // the LED settings — the ones actually touched most often — stay one click
    // from anywhere.
    if (status.connected) {
      out.push({
        id: "rim",
        name: status.wheel || "Rim",
        meta: `${status.rpm_leds || 0} rev lights`,
        online: true,
      });
    }
    return out;
  }, [status, props.devices.length, wb.changed.length]);

  const mozaPatch = (mutate: (moza: any) => void) => patch((c) => mutate(c.moza));
  const ledProps = {
    moza: config.moza,
    patch: mozaPatch,
    detectedLeds: status.rpm_leds,
    connected: status.connected,
    onPreviewButtons: props.onPreviewButtons,
    onTestLights: props.onTestLights,
    testingLights: props.testingLights,
  };

  const isRig = selected === "devices";
  const deviceTabs = selected === "rim" ? RIM_TABS : BASE_TABS;
  const tab = tabs[selected] ?? deviceTabs[0].id;
  const stagedNames = wb.changed.map((k) => wb.byKey.get(k)?.name ?? k);
  // The rig-level page is worth a look when the app cannot see the hardware the
  // user is here to configure.
  const attention = !status.connected && status.enabled;

  return (
    <div className="app-shell split">
      <AppHeader mode="hardware" onMode={props.onMode} source={false}>
        <div className="context-rule" />
        <div className="context-facts">
          <span>{status.connected ? "GARAGE" : "GARAGE · NO DEVICES"}</span>
        </div>
        <div className="spacer" />
        {/* App config: ink grey, never amber. Amber in this section means "not
            yet on the wheel" and must not also mean "not yet on disk". */}
        {props.appChanges > 0 && (
          <span className="app-chip">
            <span className="mark" />
            {props.appChanges} APP CHANGE{props.appChanges === 1 ? "" : "S"}
          </span>
        )}
        <button className="btn" onClick={props.onDiscard} disabled={props.appChanges === 0}>
          Discard
        </button>
        <button className="btn primary" onClick={props.onSave}>
          Save app config
        </button>
      </AppHeader>

      <div style={{ display: "flex", alignItems: "stretch", flex: 1, minHeight: 0, background: "var(--bg-app)" }}>
        <DeviceRail devices={rail} active={selected} onSelect={setSelected} attention={attention} />

        <div style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column" }}>
          {rail.length === 0 ? (
            <NothingAttached onRescan={props.onRescan} onPinPort={() => setSelected("devices")} />
          ) : isRig ? (
            <>
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "var(--s-6)",
                  height: 36,
                  padding: "0 var(--s-7)",
                  background: "var(--bg-inset)",
                  borderBottom: "1px solid var(--line)",
                }}
              >
                <span style={{ font: "500 11.5px var(--font-sans)", color: "var(--ink-hi)" }}>Devices &amp; ports</span>
                <span style={{ font: "400 10.5px var(--font-mono)", color: "var(--ink-lo)" }}>
                  RIG-LEVEL · NOT A DEVICE SETTING
                </span>
                <span style={{ flex: 1 }} />
                <button className="btn" onClick={props.onRescan}>
                  Rescan
                </button>
              </div>
              <Devices
                status={status}
                devices={props.devices}
                moza={config.moza}
                patch={mozaPatch}
                wb={wb}
              />
            </>
          ) : (
            <>
              <TabBar
                label={`${selected === "rim" ? "Rim" : "Wheelbase"} sections`}
                active={tab}
                onSelect={(id) => setTabs((t) => ({ ...t, [selected]: id }))}
                tabs={deviceTabs}
                // A right-aligned identity label, not a tab: its id matches
                // nothing selectable, so it never renders as the active one.
                trailing={[
                  {
                    id: "__identity",
                    label: selected === "rim" ? status.wheel || "RIM" : status.serial ? `SN ${status.serial}` : "NO SERIAL",
                    disabled: true,
                  },
                ]}
              />

              {/* The base channel only appears above tabs that write the base.
                  The LED tabs write the app's config, and a "WRITE TO BASE"
                  button above controls that do nothing of the sort would undo
                  the separation the whole layout is built on. */}
              {selected === "wheelbase" && tab !== "diag" && (
                <BaseChannel
                  online={status.connected}
                  name={status.connected ? `${status.model || "Wheelbase"} attached` : status.model || "Wheelbase"}
                  meta={status.connected ? `${status.protocol || "serial"} · ${status.port}` : "not detected"}
                  staged={stagedNames}
                  applying={wb.applying}
                  reading={wb.reading}
                  onWrite={wb.apply}
                  onRevert={wb.revert}
                  onReRead={wb.read}
                  blocked={wb.snap && !wb.snap.writable ? wb.snap.write_blocked : undefined}
                />
              )}

              {selected === "wheelbase" && tab in TAB_GROUPS && <BasePanel wb={wb} tab={tab as keyof typeof TAB_GROUPS} />}
              {selected === "wheelbase" && tab === "diag" && <Diagnostics status={status} wb={wb} />}

              {selected === "rim" && tab === "rev" && (
                <div className="set-grid" style={{ ["--set-cols" as string]: 2 }}>
                  <div className="set-col">
                    <RpmRamp {...ledProps} />
                  </div>
                  <div className="set-col">
                    <LivePreview {...ledProps} />
                  </div>
                </div>
              )}
              {selected === "rim" && tab === "buttons" && (
                <div className="set-grid" style={{ ["--set-cols" as string]: 2 }}>
                  <div className="set-col">
                    <ButtonColours {...ledProps} />
                  </div>
                  <div className="set-col">
                    <LivePreview {...ledProps} />
                  </div>
                </div>
              )}

              {selected === "pedals" && (
                <div className="set-grid" style={{ ["--set-cols" as string]: 1 }}>
                  <Panel label="PEDALS" flush>
                    <div style={{ padding: "var(--s-7)" }}>
                      <Empty title="NOT SUPPORTED YET">
                        Nothing in the app talks to a pedal set yet. They are a separate device on the same serial
                        bus as the wheelbase, so the transport is already here — the command set is not.
                      </Empty>
                    </div>
                  </Panel>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
