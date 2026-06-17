// PitMenuEditor shows and edits the in-game pit menu (fuel, virtual energy, tyres,
// pressures, wing, brakes…) via LMU's REST API (GetPitMenu / SetPitMenuValue), so
// the strategist can dial in the next stop from the pit wall. It fetches on demand
// (mount + after each change + manual refresh) rather than from the per-frame
// poll, since edits and a polled read would fight each other. Each adjustable
// component is a stepper over its option list; single-option rows (driver, damage)
// are shown read-only.
//
// The write goes through the Go client, which round-trips the whole menu as a JSON
// array — a bare object crashes the game, so the danger is kept server-side.
import { useCallback, useEffect, useState } from "react";
import { Service } from "../../../bindings/telemetry-handler/app";
import type { PitMenuItem } from "../../../bindings/telemetry-handler/game/lmu/rest";

// label strips the trailing colon LMU includes in the menu names ("FUEL RATIO:").
function label(name: string): string {
  return name.replace(/:\s*$/, "");
}

export default function PitMenuEditor() {
  const [menu, setMenu] = useState<PitMenuItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const m = await Service.GetPitMenu();
      setMenu(m);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const step = useCallback(
    async (item: PitMenuItem, delta: number) => {
      const count = item.settings?.length ?? 0;
      if (count <= 1) return;
      const next = Math.min(count - 1, Math.max(0, item.current_setting + delta));
      if (next === item.current_setting) return;
      setBusy(true);
      try {
        await Service.SetPitMenuValue(item.pmc, next);
        await load();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(false);
      }
    },
    [load],
  );

  if (error && !menu) {
    return (
      <section className="strat-group strat-pitmenu">
        <h3>Pit menu</h3>
        <p className="muted">Pit menu unavailable — {error}.</p>
      </section>
    );
  }
  if (!menu) return null;

  return (
    <section className="strat-group strat-pitmenu">
      <div className="strat-pitmenu-head">
        <h3>Pit menu</h3>
        <button className="secondary" onClick={load} disabled={busy}>Refresh</button>
      </div>
      <dl className="strat-pitmenu-list">
        {menu.map((item) => {
          const options = item.settings ?? [];
          const current = options[item.current_setting] ?? "—";
          const adjustable = options.length > 1;
          return (
            <div key={item.pmc} className="strat-pitmenu-row">
              <dt>{label(item.name)}</dt>
              <dd>
                {adjustable ? (
                  <span className="strat-stepper">
                    <button onClick={() => step(item, -1)} disabled={busy || item.current_setting <= 0} aria-label="decrease">−</button>
                    <span className="strat-stepper-val">{current}</span>
                    <button onClick={() => step(item, 1)} disabled={busy || item.current_setting >= options.length - 1} aria-label="increase">+</button>
                  </span>
                ) : (
                  <>{current}</>
                )}
              </dd>
            </div>
          );
        })}
      </dl>
      {error && <p className="muted strat-axis-note">{error}</p>}
    </section>
  );
}
