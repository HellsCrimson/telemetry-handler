// The chrome shared by both interfaces: header, context bar and tab bar.
//
// Both modes render this so the frame never moves between them — a header that
// shifts by a pixel when you switch to the pit wall is exactly the sort of thing
// that makes an app feel assembled rather than designed.

import type { ReactNode } from "react";

export type Mode = "dashboard" | "strategy";

interface HeaderProps {
  version?: string;
  mode: Mode;
  onMode: (mode: Mode) => void;
  /** Source status: the game feeding telemetry, if any. */
  source?: { label: string; rate?: string } | null;
  /** Recording status, shown only while recording. */
  recording?: string | null;
  /** Extra facts rendered between the mode toggle and the status chips. */
  children?: ReactNode;
}

export function AppHeader({ version, mode, onMode, source, recording, children }: HeaderProps) {
  return (
    <header className="app-header">
      <div className="app-brand">
        <div className="app-mark">th</div>
        <div className="app-name">telemetry-handler</div>
        {version && <div className="app-version">{version}</div>}
      </div>

      <div className="mode-toggle" role="group" aria-label="Interface">
        <button aria-pressed={mode === "dashboard"} onClick={() => onMode("dashboard")}>
          Dashboard
        </button>
        <button aria-pressed={mode === "strategy"} onClick={() => onMode("strategy")}>
          Strategy Planner
        </button>
      </div>

      {children}
      <div className="spacer" />

      {/* Source state is the first thing to check when something looks wrong, so
       * it is always present — as an explicit "no signal" chip when idle rather
       * than an absence the user has to notice. */}
      {source ? (
        <div className="chip live">
          <span className="dot" />
          <span>{source.label}</span>
          {source.rate && <span className="meta">{source.rate}</span>}
        </div>
      ) : (
        <div className="chip idle">
          <span className="dot" />
          <span>NO SIGNAL</span>
        </div>
      )}

      {recording && (
        <div className="chip rec">
          <span className="dot" />
          <span>REC {recording}</span>
        </div>
      )}
    </header>
  );
}

export interface Fact {
  label: string;
  value: string;
  unit?: string;
}

interface ContextBarProps {
  title?: string;
  kind?: string;
  facts?: Fact[];
  /** Flag state, the one place colour is allowed to fill a bar. */
  flag?: { kind: "fcy" | "red"; text: string } | null;
}

/** The context bar answers "what session am I looking at" without the user
 *  having to open a tab. Rendered only when there is a session to describe. */
export function ContextBar({ title, kind, facts, flag }: ContextBarProps) {
  return (
    <div className="context-bar">
      {title && (
        <>
          <div className="context-title">
            <strong>{title}</strong>
            {kind && <span>{kind}</span>}
          </div>
          <div className="context-rule" />
        </>
      )}
      <div className="context-facts">
        {facts?.map((f) => (
          <span key={f.label}>
            {f.label} <b>{f.value}</b>
            {f.unit ? ` ${f.unit}` : ""}
          </span>
        ))}
      </div>
      <div className="spacer" />
      {flag && (
        <div className={`flag-banner ${flag.kind}`}>
          <span className="swatch" />
          {flag.text}
        </div>
      )}
    </div>
  );
}

export interface TabDef {
  id: string;
  label: string;
  /** alert puts a dot on the tab: this page has something worth a look. */
  alert?: boolean;
  disabled?: boolean;
}

interface TabBarProps {
  tabs: readonly TabDef[];
  active: string;
  onSelect: (id: string) => void;
  /** Tabs pushed to the right — Settings, conventionally. */
  trailing?: readonly TabDef[];
  label: string;
}

export function TabBar({ tabs, active, onSelect, trailing, label }: TabBarProps) {
  const render = (tab: TabDef) => (
    <button
      key={tab.id}
      role="tab"
      aria-selected={active === tab.id}
      disabled={tab.disabled}
      onClick={() => onSelect(tab.id)}
    >
      {tab.label}
      {tab.alert && <span className="tab-alert" />}
    </button>
  );
  return (
    <nav className="tab-bar" role="tablist" aria-label={label}>
      {tabs.map(render)}
      {trailing && trailing.length > 0 && (
        <>
          <div className="spacer" />
          {trailing.map(render)}
        </>
      )}
    </nav>
  );
}

interface PanelProps {
  label: string;
  meta?: ReactNode;
  /** head renders extra controls in the panel head, right of the label. */
  head?: ReactNode;
  /** flush drops the body padding, for panels whose content is a table. */
  flush?: boolean;
  className?: string;
  style?: React.CSSProperties;
  children: ReactNode;
}

export function Panel({ label, meta, head, flush, className, style, children }: PanelProps) {
  return (
    <section className={`panel${className ? ` ${className}` : ""}`} style={style}>
      <div className="panel-head">
        <div className="panel-label">{label}</div>
        {head}
        <div className="spacer" />
        {meta && <div className="panel-meta">{meta}</div>}
      </div>
      {flush ? children : <div className="panel-body">{children}</div>}
    </section>
  );
}

export type StatTone = "" | "good" | "warn" | "crit" | "cold" | "best";

interface StatProps {
  label: string;
  value: ReactNode;
  unit?: string;
  note?: ReactNode;
  tone?: StatTone;
}

/** A single readout: label, value with unit, and an optional note carrying the
 *  comparison that makes the number mean something. */
export function Stat({ label, value, unit, note, tone = "" }: StatProps) {
  return (
    <div className={`stat${tone ? ` ${tone}` : ""}`}>
      <div className="stat-label">{label}</div>
      <div className="stat-value">
        <b>{value}</b>
        {unit && <span className="unit">{unit}</span>}
      </div>
      {note && <div className="stat-note">{note}</div>}
    </div>
  );
}

interface EmptyProps {
  title?: string;
  children: ReactNode;
  action?: ReactNode;
}

/** The state seen more than any single tab, because the game is not running
 *  most of the time the app is open. It says what is missing and what happens
 *  next — never a spinner, never fabricated zeroes. */
export function Empty({ title = "NO SIGNAL", children, action }: EmptyProps) {
  return (
    <div className="empty">
      <div className="dot" />
      <div className="empty-title">{title}</div>
      <div className="empty-body">{children}</div>
      {action}
    </div>
  );
}
