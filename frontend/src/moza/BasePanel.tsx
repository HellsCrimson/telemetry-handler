// The wheelbase's stored configuration, read-only.
//
// Every control is built from the command registry the Go side also validates
// against, so this page cannot show a range the hardware would reject. Nothing
// here writes: the controls are rendered disabled with a reason, which is the
// design system's rule for an unavailable control — never hidden, never a modal.

import { useCallback, useEffect, useState } from "react";
import { Service } from "../../bindings/telemetry-handler/app";
import { Panel, Stat, Empty } from "../design/Shell";
import { Field, FieldGroup, Slider, NumField, Toggle, Select } from "../design/Controls";

interface Command {
  key: string;
  name: string;
  unit: string;
  min: number;
  max: number;
  kind: string;
  labels?: string[];
  safety: boolean;
  verified: boolean;
  note: string;
}

interface Snapshot {
  available: boolean;
  reason: string;
  settings: Record<string, number>;
  /** Degrees Celsius; the base reports hundredths and Go converts. */
  temps: Record<string, number>;
  state: number;
  error: number;
  has_state: boolean;
  has_error: boolean;
  unsupported: Record<string, boolean>;
}

const TEMP_LABELS: Record<string, string> = {
  mcu_temp: "MCU",
  mosfet_temp: "MOSFET",
  motor_temp: "MOTOR",
};

export default function BasePanel() {
  const [commands, setCommands] = useState<Command[]>([]);
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [reading, setReading] = useState(false);

  useEffect(() => {
    Service.MozaBaseCommands()
      .then((c: any) => setCommands(c ?? []))
      .catch(() => setCommands([]));
  }, []);

  // Reading is explicit rather than polled: each read is a burst of ~27 serial
  // round trips, and polling it would fight the LED stream for the port.
  const read = useCallback(() => {
    setReading(true);
    Service.ReadMozaBase()
      .then((s: any) => setSnap(s))
      .catch((e: any) =>
        setSnap({
          available: false,
          reason: String(e),
          settings: {},
          temps: {},
          state: 0,
          error: 0,
          has_state: false,
          has_error: false,
          unsupported: {},
        }),
      )
      .finally(() => setReading(false));
  }, []);

  useEffect(read, [read]);

  if (!snap) {
    return (
      <Panel label="WHEELBASE" meta="reading…">
        <div className="empty-body">Reading the wheelbase…</div>
      </Panel>
    );
  }

  if (!snap.available) {
    return (
      <Panel label="WHEELBASE" flush>
        <div style={{ padding: "var(--s-7)" }}>
          <Empty title="NO WHEELBASE">
            {snap.reason || "The wheelbase did not answer."}
            <div style={{ marginTop: "var(--s-5)" }}>
              <button className="btn" onClick={read} disabled={reading}>
                {reading ? "Reading…" : "Retry"}
              </button>
            </div>
          </Empty>
        </div>
      </Panel>
    );
  }

  const groups = groupCommands(commands);
  const temps = Object.entries(snap.temps ?? {});
  const cells = temps.length + (snap.has_state ? 1 : 0) + (snap.has_error ? 1 : 0);

  return (
    <>
      {cells > 0 && (
        <Panel label="DEVICE" flush>
          <div className="cell-grid" style={{ gridTemplateColumns: `repeat(${cells},minmax(0,1fr))`, border: 0, borderRadius: 0 }}>
            {temps.map(([key, value]) => (
              <Stat
                key={key}
                label={TEMP_LABELS[key] ?? key.toUpperCase()}
                value={value.toFixed(1)}
                unit="°C"
                tone={value >= 70 ? "crit" : value >= 55 ? "warn" : ""}
              />
            ))}
            {snap.has_state && <Stat label="STATE" value={snap.state} note="raw" />}
            {/* A non-zero error is worth showing even before we can name the
                value, which is why it is surfaced raw rather than hidden. */}
            {snap.has_error && (
              <Stat label="ERROR" value={snap.error} note={snap.error === 0 ? "none" : "raw"} tone={snap.error === 0 ? "" : "crit"} />
            )}
          </div>
        </Panel>
      )}

      <Panel
        label="WHEELBASE SETTINGS"
        meta={
          <span style={{ display: "flex", alignItems: "center", gap: "var(--s-5)" }}>
            READ-ONLY
            <button className="btn" onClick={read} disabled={reading}>
              {reading ? "Reading…" : "Re-read"}
            </button>
          </span>
        }
        flush
      >
        {/* Says plainly why nothing can be changed here. The design system's rule
            for a disabled control is that it explains itself rather than
            vanishing — and a page of live-looking sliders that silently do
            nothing would be worse than no page. */}
        <div
          style={{
            padding: "var(--s-3) var(--s-5)",
            borderBottom: "1px solid var(--line)",
            font: "400 10px/1.45 var(--font-sans)",
            color: "var(--ink-lo)",
          }}
        >
          These are the values stored on the wheelbase. Writing them is not enabled yet — the
          ranges below still come from Boxflat's command database rather than from this base, so
          they are shown for comparison against Boxflat and Pit House first.
        </div>

        {groups.map((group) => (
          <FieldGroup key={group.title} label={group.title}>
            {group.commands.map((cmd) => (
              <ReadOnlyField
                key={cmd.key}
                cmd={cmd}
                value={snap.settings?.[cmd.key]}
                unsupported={!!snap.unsupported?.[cmd.key]}
              />
            ))}
          </FieldGroup>
        ))}
      </Panel>
    </>
  );
}

function ReadOnlyField({ cmd, value, unsupported }: { cmd: Command; value?: number; unsupported: boolean }) {
  // An unsupported command is shown, not hidden: it tells the user this base
  // does not implement the setting, which is information rather than an absence.
  // Silence is not proof of absence: at least one command on this hardware
  // replies unreliably, so the wording says what was observed rather than
  // asserting the wheel lacks the feature.
  const hint = unsupported
    ? "The base did not reply. It may not support this setting, or the reply was lost — try re-reading."
    : cmd.verified
      ? cmd.note || undefined
      : cmd.note || "Range not yet confirmed on this hardware.";

  if (unsupported || value === undefined) {
    return (
      <Field label={cmd.name} hint={hint} disabled>
        <span style={{ font: "var(--t-data)", color: "var(--ink-faint)" }}>no reply</span>
      </Field>
    );
  }

  if (cmd.kind === "bool") {
    return (
      <Field label={cmd.name} hint={hint} disabled>
        <Toggle value={value === 1} onChange={() => {}} disabled />
      </Field>
    );
  }

  if (cmd.kind === "enum") {
    // Fall back to the raw value rather than to a slider: a choice rendered as a
    // 0..100 slider looks like a percentage and invites someone to drag it,
    // which is how hands-off protection first appeared here.
    const labels = cmd.labels ?? [];
    if (labels.length === 0) {
      return (
        <Field label={cmd.name} hint={hint} disabled>
          <span style={{ font: "var(--t-data)", color: "var(--ink)" }}>{value}</span>
          <span style={{ font: "var(--t-micro)", color: "var(--ink-lo)" }}>UNLABELLED CHOICE</span>
        </Field>
      );
    }
    // A <select> whose value matches no option silently renders the FIRST one,
    // which is how protection mode read as "Mode 1" while the base was in mode 2.
    // A control that quietly shows the wrong state is worse than one that admits
    // it does not know, so an unrecognised value is surfaced as itself.
    const index = value - cmd.min;
    if (index < 0 || index >= labels.length) {
      return (
        <Field
          label={cmd.name}
          hint={`Reported ${value}, which is outside the known values (${cmd.min}..${cmd.min + labels.length - 1}). The encoding for this setting is not confirmed yet.`}
          disabled
        >
          <span style={{ font: "var(--t-data)", color: "var(--sem-warn)" }}>{value}</span>
          <span style={{ font: "var(--t-micro)", color: "var(--ink-lo)" }}>UNRECOGNISED</span>
        </Field>
      );
    }
    return (
      <Field label={cmd.name} hint={hint} disabled>
        <Select
          value={String(value)}
          options={labels.map((label, i) => ({ value: String(cmd.min + i), label }))}
          onChange={() => {}}
          disabled
        />
      </Field>
    );
  }

  // A value outside its own declared range means either the range or the
  // conversion is wrong. Showing it on a slider pinned to one end would hide
  // that; showing the number says which settings still need pinning down.
  if (value < cmd.min || value > cmd.max) {
    return (
      <Field
        label={cmd.name}
        hint={`Reported ${value}${cmd.unit ? " " + cmd.unit : ""}, outside the expected ${cmd.min}..${cmd.max}. Either the range or the conversion is wrong for this setting.`}
        disabled
      >
        <span style={{ font: "var(--t-data)", color: "var(--sem-warn)" }}>
          {value}
          {cmd.unit ? ` ${cmd.unit}` : ""}
        </span>
        <span style={{ font: "var(--t-micro)", color: "var(--ink-lo)" }}>OUT OF RANGE</span>
      </Field>
    );
  }

  return (
    <Field label={cmd.name} hint={hint} disabled>
      <Slider value={value} min={cmd.min} max={cmd.max} onChange={() => {}} disabled ariaLabel={cmd.name} />
      <NumField value={value} unit={cmd.unit} disabled onChange={() => {}} ariaLabel={cmd.name} />
    </Field>
  );
}

/** groupCommands splits the registry into the sections the design calls for.
 *  The safety envelope leads, because it is both the most consequential group
 *  and the one written first by a grouped apply. */
function groupCommands(commands: Command[]) {
  const by = (prefix: string[]) => commands.filter((c) => prefix.some((p) => c.key.startsWith(p)));
  const safety = commands.filter((c) => c.safety);
  const used = new Set(safety.map((c) => c.key));

  const take = (keys: string[]) => {
    const out = commands.filter((c) => keys.includes(c.key) && !used.has(c.key));
    out.forEach((c) => used.add(c.key));
    return out;
  };

  const groups = [
    { title: "SAFETY", commands: safety },
    { title: "GAME FORCE", commands: take(["ffb_strength", "ffb_reverse"]) },
    {
      title: "MECHANICAL FEEL",
      commands: take(["damper", "friction", "spring", "inertia", "natural_inertia", "natural_inertia_enabled"]),
    },
    { title: "SPEED DAMPING", commands: take(["speed_damping", "speed_damping_point"]) },
    { title: "GAME EFFECTS", commands: take(["game_damper", "game_friction", "game_inertia", "game_spring"]) },
    { title: "SOFT LIMIT", commands: take(["soft_limit_stiffness", "soft_limit_retain", "soft_limit_strength"]) },
    {
      title: "ROAD FEEL",
      commands: take(["road_sensitivity", "interpolation", ...by(["equalizer"]).map((c) => c.key)]),
    },
    { title: "FFB CURVE", commands: take(["ffb_curve_x1", ...by(["ffb_curve_y"]).map((c) => c.key)]) },
    { title: "PROTECTION", commands: take(["protection", "protection_mode", "performance_output"]) },
    { title: "INDICATORS & MUSIC", commands: take(["led_status", "music_enabled", "music_volume", "music_index"]) },
  ];

  // Anything the registry gains later shows up rather than silently disappearing.
  const rest = commands.filter((c) => !used.has(c.key));
  if (rest.length > 0) groups.push({ title: "OTHER", commands: rest });

  return groups.filter((g) => g.commands.length > 0);
}
