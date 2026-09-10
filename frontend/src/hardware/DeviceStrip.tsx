// Hardware presence, plus the staged-change actions for the wheelbase settings.
//
// The two belong on one row because they answer the same question: can I change
// anything right now, and is there anything waiting to be written. It sits above
// every tab in the section rather than inside one, since the answer does not
// change when you move between them.

import { Select } from "../design/Controls";
import type { Wheelbase } from "../moza/useWheelbase";
import type { MozaStatus } from "./HardwareApp";

interface Props {
  status: MozaStatus;
  wb: Wheelbase;
  onRescan: () => void;
  /** showApply hides the wheelbase actions on tabs that write nothing to the
   *  base, so the primary button never sits next to controls it does not act on. */
  showApply: boolean;
}

export default function DeviceStrip({ status, wb, onRescan, showApply }: Props) {
  const staged = wb.changed.length;

  return (
    <div className="device-strip" style={{ height: 40 }}>
      <span style={{ display: "flex", alignItems: "center", gap: "var(--s-4)" }}>
        <span
          style={{
            width: 7,
            height: 7,
            borderRadius: "50%",
            background: status.connected ? "var(--sem-good)" : "var(--ink-faint)",
          }}
        />
        <span className="name" style={{ color: status.connected ? "var(--ink-hi)" : "var(--ink-mid)" }}>
          {status.model || "MOZA wheelbase"}
        </span>
        <span style={{ color: "var(--ink-faint)" }}>
          {status.connected
            ? [status.wheel || "unknown rim", status.protocol ? `${status.protocol} protocol` : "", status.port]
                .filter(Boolean)
                .join(" · ")
            : status.enabled
              ? "NOT DETECTED"
              : "OUTPUT DISABLED"}
        </span>
      </span>

      <span style={{ width: 1, height: 16, background: "var(--line)" }} />

      {/* Presets are the next milestone. Shown disabled with the reason rather
          than omitted: a control that vanishes teaches the user nothing about
          what the app will eventually do, and one that looks live and does
          nothing is worse than either. */}
      <span style={{ display: "flex", alignItems: "center", gap: "var(--s-4)" }}>
        <span style={{ font: "var(--t-micro)", fontWeight: 600, letterSpacing: "var(--ls-label)", color: "var(--ink-faint)" }}>
          PRESET
        </span>
        <Select
          value="live"
          options={[{ value: "live", label: "Live values from the wheel" }]}
          onChange={() => {}}
          disabled
          ariaLabel="Preset"
        />
        <span style={{ font: "400 10px var(--font-sans)", color: "var(--ink-faint)" }}>
          Saved presets arrive with the preset store.
        </span>
      </span>

      {staged > 0 && (
        <span
          style={{
            display: "flex",
            alignItems: "center",
            gap: "var(--s-3)",
            height: 22,
            padding: "0 var(--s-4)",
            background: "var(--sem-warn-bg)",
            border: "1px solid var(--sem-warn-line)",
            borderRadius: 2,
            font: "600 9.5px var(--font-mono)",
            letterSpacing: ".07em",
            color: "var(--sem-warn)",
          }}
        >
          <span style={{ width: 6, height: 6, background: "var(--sem-warn)" }} />
          {staged} STAGED CHANGE{staged === 1 ? "" : "S"}
        </span>
      )}

      <span style={{ flex: 1 }} />

      {!status.connected && (
        <button className="btn" onClick={onRescan}>
          Rescan
        </button>
      )}
      {showApply && (
        <>
          <button className="btn" onClick={wb.revert} disabled={staged === 0 || wb.applying}>
            Revert
          </button>
          {/* "Apply to wheelbase", not "Save": these values go onto the device
              and stay there. The word has to say where they land. */}
          <button className="btn primary" onClick={wb.apply} disabled={staged === 0 || wb.applying}>
            {wb.applying ? "Applying…" : "Apply to wheelbase"}
          </button>
        </>
      )}
    </div>
  );
}
