// The wheelbase's stored configuration.
//
// Every control is built from the command registry the Go side also validates
// against, so this page cannot offer a value the hardware would reject. Editing
// is off unless the config allows it: unlike everything else the app writes to
// the wheel, these settings PERSIST on the hardware — they outlive the app, and
// a wrong one is still there next time the user drives, in a game that never
// asked for it.
//
// Changes are staged and applied together rather than written as you drag. A
// slider that writes on every pixel would put hundreds of writes on a serial bus
// that drops them when pushed, and would leave no moment at which the user could
// change their mind.

import { useCallback, useEffect, useMemo, useState } from "react";
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
  /** Settings this one rewrites as a side effect. */
  affects?: string[];
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
  writable: boolean;
  write_blocked: string;
}

interface ApplyResult {
  ok: boolean;
  /** Set when nothing was attempted — nothing reached the hardware. */
  refused: string;
  applied: string[];
  failed: Record<string, string>;
  /** Settings the base acknowledged but did not take, and what it reports now. */
  mismatched: Record<string, number>;
  settings: Record<string, number>;
  rewritten: Record<string, string>;
}

const TEMP_LABELS: Record<string, string> = {
  mcu_temp: "MCU",
  mosfet_temp: "MOSFET",
  motor_temp: "MOTOR",
};

const EMPTY_SNAPSHOT: Snapshot = {
  available: false,
  reason: "",
  settings: {},
  temps: {},
  state: 0,
  error: 0,
  has_state: false,
  has_error: false,
  unsupported: {},
  writable: false,
  write_blocked: "",
};

export default function BasePanel() {
  const [commands, setCommands] = useState<Command[]>([]);
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [reading, setReading] = useState(false);
  /** Staged changes, keyed by setting. Only settings the user actually moved. */
  const [draft, setDraft] = useState<Record<string, number>>({});
  const [applying, setApplying] = useState(false);
  const [result, setResult] = useState<ApplyResult | null>(null);

  useEffect(() => {
    Service.MozaBaseCommands()
      .then((c: any) => setCommands(c ?? []))
      .catch(() => setCommands([]));
  }, []);

  // Reading is explicit rather than polled: each read is a burst of ~27 serial
  // round trips, and polling it would fight the LED stream for the port.
  const read = useCallback(() => {
    setReading(true);
    setDraft({});
    setResult(null);
    Service.ReadMozaBase()
      .then((s: any) => setSnap(s))
      .catch((e: any) => setSnap({ ...EMPTY_SNAPSHOT, reason: String(e) }))
      .finally(() => setReading(false));
  }, []);

  useEffect(read, [read]);

  const byKey = useMemo(() => new Map(commands.map((c) => [c.key, c])), [commands]);
  const changed = Object.keys(draft);

  // Warn before applying, not after: road sensitivity is a macro on this
  // firmware and moves the equalizer bands with it, so a user editing both is
  // asking for two things that interact. The apply order makes the explicit
  // value win, but they should not discover that by watching sliders jump.
  const collisions = useMemo(() => {
    const out: string[] = [];
    for (const key of changed) {
      for (const affected of byKey.get(key)?.affects ?? []) {
        if (draft[affected] !== undefined) {
          out.push(`${byKey.get(key)?.name ?? key} also rewrites ${byKey.get(affected)?.name ?? affected}`);
        }
      }
    }
    return out;
  }, [changed.join(","), byKey]);

  const apply = useCallback(() => {
    setApplying(true);
    setResult(null);
    Service.ApplyMozaBase(draft)
      .then((r: any) => {
        setResult(r);
        if (r.refused) return; // nothing reached the wheel; keep the draft to fix
        // The result carries a fresh read of EVERYTHING, not just what was
        // written — a macro setting moves values the user did not touch, and the
        // page must show what the wheel now holds rather than what it hoped for.
        setSnap((s) => (s ? { ...s, settings: r.settings ?? s.settings } : s));
        // Keep staged only what did not land, so the user can see and retry it.
        const keep: Record<string, number> = {};
        for (const key of Object.keys(draft)) {
          if (r.failed?.[key] || (r.mismatched && key in r.mismatched)) keep[key] = draft[key];
        }
        setDraft(keep);
      })
      .catch((e: any) => setResult({ ...blankResult(), refused: String(e) }))
      .finally(() => setApplying(false));
  }, [draft]);

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
            {snap.writable ? (changed.length > 0 ? `${changed.length} STAGED` : "WRITABLE") : "READ-ONLY"}
            <button className="btn" onClick={read} disabled={reading || applying}>
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
        {!snap.writable && <Notice>{snap.write_blocked || "These settings cannot be changed from here."}</Notice>}
        {snap.writable && (
          <Notice>
            These values are stored on the wheelbase and outlive the app. Changes are staged until
            you apply them, then read back to confirm the base actually took them.
          </Notice>
        )}

        {result && <ResultBanner result={result} byKey={byKey} />}

        {groups.map((group) => (
          <FieldGroup key={group.title} label={group.title}>
            {group.commands.map((cmd) => (
              <SettingField
                key={cmd.key}
                cmd={cmd}
                saved={snap.settings?.[cmd.key]}
                staged={draft[cmd.key]}
                unsupported={!!snap.unsupported?.[cmd.key]}
                writable={snap.writable}
                writeBlocked={snap.write_blocked}
                onChange={(value) =>
                  setDraft((d) => {
                    const next = { ...d };
                    // A control dragged back to where it started is not a change.
                    // Applying it would spend a write on nothing and, on a macro
                    // setting, rewrite the values it affects for no reason.
                    if (value === snap.settings?.[cmd.key]) delete next[cmd.key];
                    else next[cmd.key] = value;
                    return next;
                  })
                }
              />
            ))}
          </FieldGroup>
        ))}

        {snap.writable && changed.length > 0 && (
          <ApplyBar
            count={changed.length}
            collisions={collisions}
            applying={applying}
            onApply={apply}
            onRevert={() => setDraft({})}
          />
        )}
      </Panel>
    </>
  );
}

function blankResult(): ApplyResult {
  return { ok: false, refused: "", applied: [], failed: {}, mismatched: {}, settings: {}, rewritten: {} };
}

function Notice({ children }: { children: React.ReactNode }) {
  return (
    <div
      style={{
        padding: "var(--s-3) var(--s-5)",
        borderBottom: "1px solid var(--line)",
        font: "400 10px/1.45 var(--font-sans)",
        color: "var(--ink-lo)",
      }}
    >
      {children}
    </div>
  );
}

/** The staged-changes bar. Sticky rather than at the end of a long page: the
 *  user must be able to apply or abandon from wherever they made the change. */
function ApplyBar({
  count,
  collisions,
  applying,
  onApply,
  onRevert,
}: {
  count: number;
  collisions: string[];
  applying: boolean;
  onApply: () => void;
  onRevert: () => void;
}) {
  return (
    <div
      style={{
        position: "sticky",
        bottom: 0,
        display: "flex",
        alignItems: "center",
        gap: "var(--s-5)",
        padding: "var(--s-4) var(--s-5)",
        borderTop: "1px solid var(--line)",
        background: "var(--bg-inset)",
      }}
    >
      <span style={{ font: "var(--t-micro)", color: "var(--ink-lo)", letterSpacing: ".08em" }}>
        {count} STAGED CHANGE{count === 1 ? "" : "S"}
      </span>
      {collisions.length > 0 && (
        <span style={{ font: "400 10px/1.45 var(--font-sans)", color: "var(--sem-warn)" }}>
          {collisions.join("; ")} — the explicit value is written last and wins.
        </span>
      )}
      <span style={{ marginLeft: "auto", display: "flex", gap: "var(--s-4)" }}>
        <button className="btn" onClick={onRevert} disabled={applying}>
          Revert
        </button>
        <button className="btn primary" onClick={onApply} disabled={applying}>
          {applying ? "Applying…" : "Apply to wheelbase"}
        </button>
      </span>
    </div>
  );
}

/** What came of the last apply. A partial apply is a normal outcome, not an
 *  exception, so this says per setting what is live and what is not rather than
 *  collapsing to success or failure. */
function ResultBanner({ result, byKey }: { result: ApplyResult; byKey: Map<string, Command> }) {
  const name = (key: string) => byKey.get(key)?.name ?? key;
  const failed = Object.entries(result.failed ?? {});
  const mismatched = Object.entries(result.mismatched ?? {});
  const tone = result.refused || failed.length > 0 || mismatched.length > 0 ? "crit" : "good";

  return (
    <div
      style={{
        padding: "var(--s-4) var(--s-5)",
        borderBottom: "1px solid var(--line)",
        background: tone === "good" ? "var(--sem-good-bg)" : "var(--sem-crit-bg)",
        font: "400 10px/1.5 var(--font-sans)",
        color: "var(--ink)",
      }}
    >
      {result.refused ? (
        <>
          <strong style={{ color: "var(--sem-crit)" }}>Nothing was written.</strong> {result.refused}
        </>
      ) : (
        <>
          <strong style={{ color: tone === "good" ? "var(--sem-good)" : "var(--sem-warn)" }}>
            {result.applied?.length ?? 0} setting{result.applied?.length === 1 ? "" : "s"} written
            {result.ok ? " and confirmed." : "."}
          </strong>
          {failed.map(([key, why]) => (
            <div key={key} style={{ color: "var(--sem-crit)" }}>
              {name(key)}: {why}
            </div>
          ))}
          {/* The failure the read-back exists to catch: the base said yes and did
              nothing. Reported separately from a failed write because the user
              would otherwise believe in a change that never happened. */}
          {mismatched.map(([key, got]) => (
            <div key={key} style={{ color: "var(--sem-crit)" }}>
              {name(key)}: the base acknowledged the write but still reports {got}.
            </div>
          ))}
          {Object.entries(result.rewritten ?? {}).map(([key, by]) => (
            <div key={key} style={{ color: "var(--ink-lo)" }}>
              {name(key)} was also rewritten by {name(by)}.
            </div>
          ))}
        </>
      )}
    </div>
  );
}

/** lockedReason explains why a setting cannot be edited, or returns null when it
 *  can. Every branch below is a case where writing would be a guess: the page
 *  says so rather than offering a control that means something else. */
function lockedReason(cmd: Command, saved: number | undefined, unsupported: boolean, writable: boolean, writeBlocked: string): string | null {
  if (unsupported || saved === undefined) {
    // Silence is not proof of absence: at least one command on this hardware
    // replies unreliably, so the wording says what was observed rather than
    // asserting the wheel lacks the feature.
    return "The base did not reply. It may not support this setting, or the reply was lost — try re-reading.";
  }
  if (!cmd.verified) {
    // An unconfirmed conversion means we do not know what number actually
    // reaches the base. Writing 20 and having it land as 200 is the accident
    // worth designing against, so these stay read-only until measured.
    return cmd.note
      ? `${cmd.note}. Not writable yet: the conversion is not confirmed on this hardware.`
      : "Not writable yet: the range and scaling for this setting are not confirmed on this hardware.";
  }
  if (cmd.kind === "enum" && (cmd.labels?.length ?? 0) === 0) {
    return "The choices for this setting are not named yet.";
  }
  if (cmd.kind === "enum" && (saved - cmd.min < 0 || saved - cmd.min >= (cmd.labels?.length ?? 0))) {
    return `Reported ${saved}, which is outside the known values (${cmd.min}..${cmd.min + (cmd.labels?.length ?? 1) - 1}). The encoding for this setting is not confirmed yet.`;
  }
  if (cmd.kind !== "enum" && cmd.kind !== "bool" && (saved < cmd.min || saved > cmd.max)) {
    return `Reported ${saved}${cmd.unit ? " " + cmd.unit : ""}, outside the expected ${cmd.min}..${cmd.max}. Either the range or the conversion is wrong for this setting.`;
  }
  if (!writable) return writeBlocked || "Read-only.";
  return null;
}

function SettingField({
  cmd,
  saved,
  staged,
  unsupported,
  writable,
  writeBlocked,
  onChange,
}: {
  cmd: Command;
  saved?: number;
  staged?: number;
  unsupported: boolean;
  writable: boolean;
  writeBlocked: string;
  onChange: (value: number) => void;
}) {
  const locked = lockedReason(cmd, saved, unsupported, writable, writeBlocked);
  const value = staged ?? saved;
  const drifted = staged !== undefined;
  const hint = locked ?? cmd.note ?? undefined;

  if (unsupported || saved === undefined) {
    return (
      <Field label={cmd.name} hint={hint} disabled>
        <span style={{ font: "var(--t-data)", color: "var(--ink-faint)" }}>no reply</span>
      </Field>
    );
  }

  // A value the control cannot represent is shown as itself rather than
  // approximated. A <select> whose value matches no option silently renders the
  // FIRST one, which is how protection mode read as "Mode 1" while the base was
  // in mode 2; a slider pinned to one end hides an out-of-range reading the same
  // way. A control that quietly shows the wrong state is worse than one that
  // admits it does not know.
  const unrepresentable =
    (cmd.kind === "enum" && (saved - cmd.min < 0 || saved - cmd.min >= (cmd.labels?.length ?? 0))) ||
    (cmd.kind !== "enum" && cmd.kind !== "bool" && (saved < cmd.min || saved > cmd.max));
  if (unrepresentable) {
    return (
      <Field label={cmd.name} hint={hint} disabled>
        <span style={{ font: "var(--t-data)", color: "var(--sem-warn)" }}>
          {saved}
          {cmd.unit && cmd.kind !== "enum" ? ` ${cmd.unit}` : ""}
        </span>
        <span style={{ font: "var(--t-micro)", color: "var(--ink-lo)" }}>
          {cmd.kind === "enum" ? "UNRECOGNISED" : "OUT OF RANGE"}
        </span>
      </Field>
    );
  }

  const delta = drifted ? `${saved} → ${staged}` : undefined;

  if (cmd.kind === "bool") {
    return (
      <Field label={cmd.name} hint={hint} delta={delta} drifted={drifted} disabled={!!locked}>
        <Toggle value={value === 1} onChange={(on) => onChange(on ? 1 : 0)} disabled={!!locked} />
      </Field>
    );
  }

  if (cmd.kind === "enum") {
    const labels = cmd.labels ?? [];
    if (labels.length === 0) {
      return (
        <Field label={cmd.name} hint={hint} disabled>
          <span style={{ font: "var(--t-data)", color: "var(--ink)" }}>{saved}</span>
          <span style={{ font: "var(--t-micro)", color: "var(--ink-lo)" }}>UNLABELLED CHOICE</span>
        </Field>
      );
    }
    return (
      <Field label={cmd.name} hint={hint} delta={delta} drifted={drifted} disabled={!!locked}>
        <Select
          value={String(value)}
          options={labels.map((label, i) => ({ value: String(cmd.min + i), label }))}
          onChange={(v) => onChange(Number(v))}
          disabled={!!locked}
        />
      </Field>
    );
  }

  return (
    <Field label={cmd.name} hint={hint} delta={delta} drifted={drifted} disabled={!!locked}>
      <Slider
        value={value ?? 0}
        min={cmd.min}
        max={cmd.max}
        saved={saved}
        onChange={onChange}
        disabled={!!locked}
        ariaLabel={cmd.name}
      />
      <NumField
        value={value ?? 0}
        unit={cmd.unit}
        min={cmd.min}
        max={cmd.max}
        drifted={drifted}
        disabled={!!locked}
        onChange={onChange}
        ariaLabel={cmd.name}
      />
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
