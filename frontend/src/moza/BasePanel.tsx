// The wheelbase's stored settings — artboard 3a.
//
// Every control is built from the command registry the Go side also validates
// against, so this page cannot offer a value the hardware would reject.
//
// The layout problem this solves: 41 settings in 10 groups. Almost all are
// writable now, but any row can still turn read-only at runtime — the base did
// not answer, it reported a value outside the known range, or (for the one
// command nothing documents) the conversion is unconfirmed — and writing a
// number we cannot vouch for is the accident the whole milestone was designed
// against. So a READ-ONLY ROW KEEPS ITS ROW — same height, same label column —
// with the reason where the control would be and an RO tick in the gutter.
// Inert, not broken. The filter above collapses them when you are tuning.
//
// State lives in useWheelbase, not here: the base channel above the tabs carries
// the same staged count and the button that writes it.

import { useMemo, useState } from "react";
import { Panel, Empty } from "../design/Shell";
import { Slider, Toggle, Select } from "../design/Controls";
import type { Command, Wheelbase } from "./useWheelbase";

/** The groups each device tab shows, in order. Splitting 41 settings across
 *  tabs is what stops the page being one column you scroll past Game Effects to
 *  get through. */
export const TAB_GROUPS: Record<string, string[]> = {
  base: ["GAME FORCE", "MECHANICAL FEEL", "SPEED DAMPING", "GAME EFFECTS", "ROAD FEEL", "FFB CURVE"],
  limits: ["SAFETY", "SOFT LIMIT", "PROTECTION"],
  indicators: ["INDICATORS & MUSIC"],
};

type Filter = "all" | "writable" | "staged";

export default function BasePanel({ wb, tab }: { wb: Wheelbase; tab: keyof typeof TAB_GROUPS }) {
  const { snap, commands, draft, reading, result, byKey, stage } = wb;
  const [filter, setFilter] = useState<Filter>("all");
  const [query, setQuery] = useState("");

  const groups = useMemo(() => groupCommands(commands, TAB_GROUPS[tab] ?? []), [commands, tab]);

  if (!snap) {
    return (
      <div className="set-grid" style={{ ["--set-cols" as string]: 1 }}>
        <Panel label="WHEELBASE" meta="reading…">
          <div className="empty-body">Reading the wheelbase…</div>
        </Panel>
      </div>
    );
  }

  if (!snap.available) {
    // Disconnected is a STATE, not an error. The frame stays; only what it holds
    // changes, because the base being switched off is ordinary.
    return (
      <div className="set-grid" style={{ ["--set-cols" as string]: 1 }}>
        <Panel label="WHEELBASE SETTINGS" meta="OFFLINE" flush>
          <div style={{ padding: "var(--s-7)" }}>
            <Empty title="NO WHEELBASE">
              {snap.reason || "The wheelbase did not answer."}
              <div style={{ marginTop: "var(--s-5)" }}>
                <button className="btn" onClick={wb.read} disabled={reading}>
                  {reading ? "Reading…" : "Retry"}
                </button>
              </div>
            </Empty>
          </div>
        </Panel>
      </div>
    );
  }

  const rows = groups.flatMap((g) => g.commands);
  const writable = rows.filter((c) => !lockedReason(c, snap.settings?.[c.key], !!snap.unsupported?.[c.key], snap.writable, snap.write_blocked));

  const visible = groups
    .map((g) => ({
      ...g,
      commands: g.commands.filter((cmd) => {
        if (query && !cmd.name.toLowerCase().includes(query.toLowerCase())) return false;
        if (filter === "staged") return draft[cmd.key] !== undefined;
        if (filter === "writable") return writable.includes(cmd);
        return true;
      }),
    }))
    .filter((g) => g.commands.length > 0);

  // Three balanced columns, filled by height rather than by count: the groups
  // are wildly different sizes (Road Feel has eight rows, Speed Damping two) and
  // dealing them round-robin would leave one column half empty.
  const columns = balance(visible, 3);

  return (
    <>
      <div className="set-filter">
        <span>
          {rows.length} SETTING{rows.length === 1 ? "" : "S"} · <span style={{ color: "var(--ink-hi)" }}>{writable.length} WRITABLE</span> ·{" "}
          <span style={{ color: "var(--ink-lo)" }}>{rows.length - writable.length} READ-ONLY</span>
        </span>
        <div className="segmented" role="group" aria-label="Filter settings">
          {(["all", "writable", "staged"] as Filter[]).map((f) => (
            <button key={f} aria-pressed={filter === f} onClick={() => setFilter(f)}>
              {f === "all" ? "All" : f === "writable" ? "Writable only" : "Staged"}
            </button>
          ))}
        </div>
        <span className="hint">Read-only rows show the reason the base gave.</span>
        <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Filter settings" aria-label="Filter settings by name" />
      </div>

      {result && <ResultBanner result={result} byKey={byKey} onDismiss={wb.dismissResult} />}
      {!snap.writable && (
        <div
          style={{
            padding: "var(--s-3) var(--s-7)",
            borderBottom: "1px solid var(--line)",
            font: "400 10px/1.45 var(--font-sans)",
            color: "var(--ink-lo)",
          }}
        >
          {snap.write_blocked || "These settings cannot be changed from here."}
        </div>
      )}

      {visible.length === 0 ? (
        <div style={{ padding: "var(--s-7)" }}>
          <Empty title="NOTHING MATCHES">
            No setting on this tab matches the filter. Clear it to see the rest.
          </Empty>
        </div>
      ) : (
        <div className="set-grid">
          {columns.map((col, i) => (
            <div className="set-col" key={i}>
              {col.map((group) => (
                <Panel
                  key={group.title}
                  label={group.title}
                  head={<span className="target-badge base">BASE MEMORY</span>}
                  meta={`${group.commands.length}`}
                  flush
                >
                  <div className="set-body">
                    {group.commands.map((cmd) => (
                      <SettingRow
                        key={cmd.key}
                        cmd={cmd}
                        saved={snap.settings?.[cmd.key]}
                        staged={draft[cmd.key]}
                        unsupported={!!snap.unsupported?.[cmd.key]}
                        writable={snap.writable}
                        writeBlocked={snap.write_blocked}
                        onChange={(value) => stage(cmd.key, value)}
                      />
                    ))}
                  </div>
                </Panel>
              ))}
            </div>
          ))}
        </div>
      )}
    </>
  );
}

/** balance deals groups into n columns, always adding the next group to the
 *  shortest column, so the three columns end up roughly level. */
function balance<T extends { commands: unknown[] }>(groups: T[], n: number): T[][] {
  const cols: T[][] = Array.from({ length: n }, () => []);
  const heights = new Array(n).fill(0);
  for (const g of groups) {
    let at = 0;
    for (let i = 1; i < n; i++) if (heights[i] < heights[at]) at = i;
    cols[at].push(g);
    heights[at] += g.commands.length + 2; // + panel head and its own gap
  }
  return cols;
}

/** lockedReason explains why a setting cannot be edited, or returns null when it
 *  can. Every branch is a case where writing would be a guess: the row says so
 *  rather than offering a control that means something else. */
export function lockedReason(
  cmd: Command,
  saved: number | undefined,
  unsupported: boolean,
  writable: boolean,
  writeBlocked: string,
): string | null {
  if (unsupported || saved === undefined) {
    // Silence is not proof of absence: at least one command on this hardware
    // replies unreliably, so the wording says what was observed rather than
    // asserting the wheel lacks the feature.
    return "No reply — may not be supported, or the reply was lost.";
  }
  if (!cmd.verified) {
    // An unconfirmed conversion means we do not know what number actually
    // reaches the base. Writing 20 and having it land as 200 is the accident
    // worth designing against, so these stay read-only until measured.
    return "Conversion not confirmed on this hardware.";
  }
  if (cmd.kind === "enum" && (cmd.labels?.length ?? 0) === 0) {
    return "The choices for this setting are not named yet.";
  }
  if (cmd.kind === "enum" && !inEnum(cmd, saved)) {
    return `Reported ${saved}, outside the known values — encoding unconfirmed.`;
  }
  if (isNumeric(cmd) && (saved < cmd.min || saved > cmd.max)) {
    return `Reported ${saved}, outside the expected ${cmd.min}..${cmd.max}.`;
  }
  if (!writable) return writeBlocked || "Read-only.";
  return null;
}

const isNumeric = (cmd: Command) => cmd.kind !== "enum" && cmd.kind !== "bool";
const inEnum = (cmd: Command, v: number) => v - cmd.min >= 0 && v - cmd.min < (cmd.labels?.length ?? 0);

function SettingRow({
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

  if (locked) {
    // The value still shows. What the base holds is worth knowing even when we
    // cannot safely change it — that is the whole read-only milestone.
    const odd =
      saved !== undefined &&
      ((cmd.kind === "enum" && !inEnum(cmd, saved)) || (isNumeric(cmd) && (saved < cmd.min || saved > cmd.max)));
    return (
      <div className="set-row ro">
        <div className="set-label">{cmd.name}</div>
        <div className="set-reason">{locked}</div>
        <div className={`set-value flat${odd ? " odd" : ""}`}>
          <input value={saved === undefined ? "—" : String(saved)} readOnly tabIndex={-1} aria-label={cmd.name} />
        </div>
        <div className="set-gutter">RO</div>
      </div>
    );
  }

  if (cmd.kind === "bool") {
    return (
      <div className={`set-row${drifted ? " staged" : ""}`}>
        <div className="set-label">{cmd.name}</div>
        <div className="set-control">
          <Toggle value={value === 1} onChange={(on) => onChange(on ? 1 : 0)} ariaLabel={cmd.name} />
          {cmd.note && <span className="set-note">{cmd.note}</span>}
        </div>
        <div />
        <div className="set-gutter">{drifted ? (value === 1 ? "ON" : "OFF") : ""}</div>
      </div>
    );
  }

  if (cmd.kind === "enum") {
    const labels = cmd.labels ?? [];
    return (
      <div className={`set-row${drifted ? " staged" : ""}`}>
        <div className="set-label">{cmd.name}</div>
        <div className="set-control">
          <Select
            value={String(value)}
            options={labels.map((label, i) => ({ value: String(cmd.min + i), label }))}
            onChange={(v) => onChange(Number(v))}
            ariaLabel={cmd.name}
          />
          {cmd.note && <span className="set-note">{cmd.note}</span>}
        </div>
        <div />
        <div className="set-gutter">{drifted ? "SET" : ""}</div>
      </div>
    );
  }

  return (
    <div className={`set-row${drifted ? " staged" : ""}`}>
      <div className="set-label">{cmd.name}</div>
      <div className="set-control">
        {/* saved draws the ghost mark. Without it "staged" says something
            changed but not from what. */}
        <Slider value={value ?? 0} min={cmd.min} max={cmd.max} saved={saved} onChange={onChange} ariaLabel={cmd.name} />
      </div>
      <div className={`set-value${drifted ? " staged" : ""}`}>
        <input
          type="number"
          value={value ?? 0}
          min={cmd.min}
          max={cmd.max}
          onChange={(e) => {
            const next = Number(e.target.value);
            if (Number.isFinite(next)) onChange(next);
          }}
          aria-label={cmd.name}
        />
        {cmd.unit && <span className="unit">{cmd.unit}</span>}
      </div>
      <div className="set-gutter">{drifted && saved !== undefined ? signed(value! - saved) : ""}</div>
    </div>
  );
}

const signed = (n: number) => `${n > 0 ? "+" : ""}${Math.round(n * 100) / 100}`;

/** What came of the last write. A partial write is a normal outcome, not an
 *  exception, so this says per setting what is live and what is not rather than
 *  collapsing to success or failure. */
function ResultBanner({
  result,
  byKey,
  onDismiss,
}: {
  result: import("./useWheelbase").ApplyResult;
  byKey: Map<string, Command>;
  onDismiss: () => void;
}) {
  const name = (key: string) => byKey.get(key)?.name ?? key;
  const failed = Object.entries(result.failed ?? {});
  const mismatched = Object.entries(result.mismatched ?? {});
  const bad = result.refused || failed.length > 0 || mismatched.length > 0;

  return (
    <div
      style={{
        display: "flex",
        gap: "var(--s-5)",
        padding: "var(--s-4) var(--s-7)",
        borderBottom: "1px solid var(--line)",
        background: bad ? "var(--sem-crit-bg)" : "var(--sem-good-bg)",
        font: "400 10px/1.5 var(--font-sans)",
        color: "var(--ink)",
      }}
    >
      <div style={{ flex: 1, minWidth: 0 }}>
        {result.refused ? (
          <>
            <strong style={{ color: "var(--sem-crit)" }}>Nothing was written.</strong> {result.refused}
          </>
        ) : (
          <>
            <strong style={{ color: bad ? "var(--sem-warn)" : "var(--sem-good)" }}>
              {result.applied?.length ?? 0} setting{result.applied?.length === 1 ? "" : "s"} written to the base
              {result.ok ? " and confirmed." : "."}
            </strong>
            {failed.map(([key, why]) => (
              <div key={key} style={{ color: "var(--sem-crit)" }}>
                {name(key)}: {why}
              </div>
            ))}
            {/* The failure the read-back exists to catch: the base said yes and
                did nothing. Reported separately from a failed write, because the
                user would otherwise believe in a change that never happened. */}
            {mismatched.map(([key, got]) => (
              <div key={key} style={{ color: "var(--sem-crit)" }}>
                {name(key)}: the base acknowledged the write but still reports {got}.
              </div>
            ))}
            {/* Say what is certain — the order — rather than claiming the base
                rewrote anything: whether road sensitivity moves the bands on its
                own is unconfirmed, and the fresh read above shows what actually
                happened either way. */}
            {Object.entries(result.rewritten ?? {}).map(([key, by]) => (
              <div key={key} style={{ color: "var(--ink-lo)" }}>
                {name(key)} was written after {name(by)}, so your value for it is the one that stands.
              </div>
            ))}
          </>
        )}
      </div>
      <button className="btn" onClick={onDismiss} style={{ flex: "none", alignSelf: "flex-start" }}>
        Dismiss
      </button>
    </div>
  );
}

/** groupCommands splits the registry into named groups, then keeps only the ones
 *  this tab shows. Anything the registry gains later lands in OTHER rather than
 *  silently disappearing. */
function groupCommands(commands: Command[], wanted: string[]) {
  const by = (prefix: string[]) => commands.filter((c) => prefix.some((p) => c.key.startsWith(p)));
  const safety = commands.filter((c) => c.safety);
  const used = new Set(safety.map((c) => c.key));

  const take = (keys: string[]) => {
    const out = commands.filter((c) => keys.includes(c.key) && !used.has(c.key));
    out.forEach((c) => used.add(c.key));
    return out;
  };

  const all = [
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

  const rest = commands.filter((c) => !used.has(c.key));
  if (rest.length > 0) all.push({ title: "OTHER", commands: rest });

  // OTHER rides along with the base tab so a newly added command is never
  // stranded on a tab nobody opens.
  const keep = wanted.includes("GAME FORCE") ? [...wanted, "OTHER"] : wanted;
  return all.filter((g) => keep.includes(g.title) && g.commands.length > 0);
}
