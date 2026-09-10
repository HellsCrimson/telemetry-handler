// The wheelbase settings' state, lifted out of the panel that renders them.
//
// It lives in a hook because two parts of the MOZA page need it at once: the
// device strip at the top carries the staged-change count and the Apply/Revert
// actions, while the panels below carry the controls. Duplicating the state
// would let the strip and the fields disagree about what is staged, which is
// exactly the thing the amber drift marking exists to make impossible.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Service } from "../../bindings/telemetry-handler/app";

export interface Command {
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

export interface Snapshot {
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

export interface ApplyResult {
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

export const EMPTY_SNAPSHOT: Snapshot = {
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

export interface Wheelbase {
  commands: Command[];
  byKey: Map<string, Command>;
  snap: Snapshot | null;
  reading: boolean;
  applying: boolean;
  result: ApplyResult | null;
  /** Staged changes, keyed by setting. Only settings actually moved. */
  draft: Record<string, number>;
  changed: string[];
  /** Settings in the draft that another staged setting will overwrite. */
  collisions: string[];
  read: () => void;
  stage: (key: string, value: number) => void;
  revert: () => void;
  apply: () => void;
  dismissResult: () => void;
}

/** active is true while the MOZA page is on screen.
 *
 *  The hook lives ABOVE the page rather than inside it so staged changes survive
 *  a tab switch — a user who stages three settings, checks a lap time and comes
 *  back should not find their edits quietly gone. But the first read is a burst
 *  of ~27 serial round trips that would fight the LED stream, so it waits for
 *  the page to actually be opened rather than running at startup. */
export function useWheelbase(active: boolean): Wheelbase {
  const [commands, setCommands] = useState<Command[]>([]);
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [reading, setReading] = useState(false);
  const [draft, setDraft] = useState<Record<string, number>>({});
  const [applying, setApplying] = useState(false);
  const [result, setResult] = useState<ApplyResult | null>(null);

  useEffect(() => {
    if (!active) return;
    Service.MozaBaseCommands()
      .then((c: any) => setCommands(c ?? []))
      .catch(() => setCommands([]));
  }, [active]);

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

  // Read once, on the first visit. Re-reading on every visit would throw away
  // whatever the user has staged, and the values only change when this page or
  // another tool writes them.
  const opened = useRef(false);
  useEffect(() => {
    if (!active || opened.current) return;
    opened.current = true;
    read();
  }, [active, read]);

  const byKey = useMemo(() => new Map(commands.map((c) => [c.key, c])), [commands]);
  const changed = Object.keys(draft);

  const stage = useCallback(
    (key: string, value: number) =>
      setDraft((d) => {
        const next = { ...d };
        // A control dragged back to where it started is not a change. Applying it
        // would spend a write on nothing and, on a macro setting, rewrite the
        // values it affects for no reason.
        if (value === snap?.settings?.[key]) delete next[key];
        else next[key] = value;
        return next;
      }),
    [snap],
  );

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
        // Keep staged only what did not land, so it can be seen and retried.
        const keep: Record<string, number> = {};
        for (const key of Object.keys(draft)) {
          if (r.failed?.[key] || (r.mismatched && key in r.mismatched)) keep[key] = draft[key];
        }
        setDraft(keep);
      })
      .catch((e: any) => setResult({ ...blankResult(), refused: String(e) }))
      .finally(() => setApplying(false));
  }, [draft]);

  return {
    commands,
    byKey,
    snap,
    reading,
    applying,
    result,
    draft,
    changed,
    collisions,
    read,
    stage,
    revert: () => setDraft({}),
    apply,
    dismissResult: () => setResult(null),
  };
}

export function blankResult(): ApplyResult {
  return { ok: false, refused: "", applied: [], failed: {}, mismatched: {}, settings: {}, rewritten: {} };
}
