// The device rail: presence, one row per attached device.
//
// This is the answer to how the section scales from one device to five. The rail
// lists WHAT IS ATTACHED; the tab bar beside it lists WHAT YOU ARE ADJUSTING on
// the selected one. A flat capability bar breaks the moment two devices both
// have LEDs — "LEDs" would be ambiguous — whereas a device-scoped tab never is.
//
// Each row carries its own state dot and its own staged count, so leaving a
// device cannot hide that something is still waiting to be written to it.
//
// Rig-level pages sit at the foot below a hairline, because they are not
// capabilities of any one device.

export interface RailDevice {
  id: string;
  name: string;
  meta: string;
  online: boolean;
  /** Settings staged against this device, if it holds any of its own. */
  staged?: number;
}

interface Props {
  devices: RailDevice[];
  active: string;
  onSelect: (id: string) => void;
  /** attention marks a rig-level page that has something worth a look. */
  attention?: boolean;
}

export default function DeviceRail({ devices, active, onSelect, attention }: Props) {
  const online = devices.filter((d) => d.online).length;

  return (
    <nav className="dev-rail" aria-label="Devices">
      <div className="dev-rail-head">
        DEVICES
        <span className="count">{devices.length === 0 ? "0" : `${online} / ${devices.length}`}</span>
      </div>

      {devices.length === 0 ? (
        // Not an error. A rig with nothing plugged in is the first thing a new
        // user sees, and it should read as waiting rather than broken.
        <div className="dev-rail-empty">
          <div
            style={{
              width: 7,
              height: 7,
              borderRadius: "50%",
              background: "var(--ink-lo)",
              margin: "0 auto var(--s-4)",
              animation: "hw-pulse 1.8s ease-in-out infinite",
            }}
          />
          <div style={{ font: "600 9.5px var(--font-mono)", letterSpacing: "var(--ls-label)", color: "var(--ink-mid)", marginBottom: 5 }}>
            NONE FOUND
          </div>
          <div style={{ font: "400 10px/1.45 var(--font-sans)", color: "var(--ink-lo)" }}>
            Devices appear here as soon as one is plugged in.
          </div>
        </div>
      ) : (
        <div className="dev-rail-list">
          {devices.map((d) => (
            <button
              key={d.id}
              className={`dev-row${d.online ? " online" : ""}`}
              aria-selected={active === d.id}
              role="tab"
              onClick={() => onSelect(d.id)}
            >
              <span className="dot" />
              <span className="body">
                <span className="name">{d.name}</span>
                <span className="meta">{d.meta}</span>
              </span>
              {d.staged ? <span className="badge">{d.staged}</span> : null}
            </button>
          ))}
        </div>
      )}

      <div className="dev-rail-foot">
        <button className="dev-row" aria-selected={active === "devices"} role="tab" onClick={() => onSelect("devices")}>
          <span className="dot" style={{ background: "var(--ink-lo)" }} />
          <span className="body">
            <span className="name">Devices &amp; ports</span>
          </span>
          {attention && <span className="attn" />}
        </button>
        {/* Firmware has no backend at all. Present so the rail's shape is honest
            about what it will hold, disabled so it cannot pretend otherwise. */}
        <button className="dev-row" disabled title="No firmware support yet.">
          <span className="dot" style={{ background: "var(--line)" }} />
          <span className="body">
            <span className="name">Firmware</span>
          </span>
        </button>
      </div>
    </nav>
  );
}
