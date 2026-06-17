// SetupSheet shows — and now edits — the player car's active setup (aero,
// suspension, gearing, tyres, brakes, engine maps), read from / written to LMU's
// REST API (GetCarSetup / SetSetupValue). It is the one strategy view NOT fed by
// the per-frame SessionState: the setup only changes in the garage, so it is
// fetched on demand (on mount + after each edit + a manual refresh). Each setting
// is a stepper that writes the new step index and re-reads the sheet so the
// game-formatted value (and any clamping) is reflected.
import { useCallback, useEffect, useState } from "react";
import { Service } from "../../../bindings/telemetry-handler/app";
import { type CarSetup, type SetupSetting } from "../model";

// prettyCategory turns "SUSPENSION_FRONT" into "Suspension Front".
function prettyCategory(cat: string): string {
  return cat
    .toLowerCase()
    .split("_")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

// settingLabel uses the game caption when present, else a tidied key (dropping the
// VM_/WM_ prefix and the -W_xx wheel suffix the tyre settings carry).
function settingLabel(s: SetupSetting): string {
  if (s.caption) return s.caption;
  return s.key
    .replace(/^(VM_|WM_)/, "")
    .replace(/-W_(FL|FR|RL|RR)$/, "")
    .replace(/_/g, " ");
}

export default function SetupSheet() {
  const [setup, setSetup] = useState<CarSetup | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const s = await Service.GetCarSetup();
      setSetup(s);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // step writes a setting's new step index then re-reads the sheet so the
  // game-formatted string value (and any clamping) shows. Writes are serialised
  // via `busy` so two quick clicks can't race.
  const step = useCallback(
    async (s: SetupSetting, delta: number) => {
      const next = Math.min(s.max_value, Math.max(s.min_value, Math.round(s.value) + delta));
      if (next === s.value) return;
      setBusy(true);
      try {
        await Service.SetSetupValue(s.key, next);
        await load();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(false);
      }
    },
    [load],
  );

  const fixed = !!setup?.fixed_setup_race;

  return (
    <div className="strat-setup">
      <div className="strat-setup-bar">
        <div>
          {setup ? (
            <>
              <strong>{setup.active_setup || "—"}</strong>
              {setup.unsaved_changes && <span className="strat-setup-badge">unsaved changes</span>}
              {fixed && <span className="strat-setup-badge fixed">fixed setup</span>}
              <p className="muted">
                {setup.car.display_name || setup.car.name}
                {setup.car.class ? ` · ${setup.car.class}` : ""}
                {setup.track_folder ? ` · ${setup.track_folder}` : ""}
              </p>
            </>
          ) : (
            <strong>Car setup</strong>
          )}
        </div>
        <button className="secondary" onClick={load} disabled={loading || busy}>
          {loading ? "Loading…" : "Refresh"}
        </button>
      </div>

      {fixed && <p className="muted strat-axis-note">This session uses a fixed setup — values can’t be changed.</p>}

      {error && (
        <p className="muted">
          Setup unavailable — {error}. The setup is read from LMU; make sure the game is running and a car is loaded.
        </p>
      )}

      {setup && setup.groups.length > 0 && (
        <div className="strat-livedata">
          {setup.groups.map((g) => (
            <section className="strat-group strat-setup-group" key={g.category}>
              <h3>{prettyCategory(g.category)}</h3>
              <dl className="strat-setup-list">
                {g.settings.map((s) => {
                  const adjustable = !fixed && s.max_value > s.min_value;
                  return (
                    <div key={s.key} className={`strat-setup-row${s.changed ? " changed" : ""}`}>
                      <dt title={s.key}>{settingLabel(s)}</dt>
                      <dd>
                        {adjustable ? (
                          <span className="strat-stepper">
                            <button onClick={() => step(s, -1)} disabled={busy || s.value <= s.min_value} aria-label="decrease">−</button>
                            <span className="strat-stepper-val">{s.string_value || "—"}</span>
                            <button onClick={() => step(s, 1)} disabled={busy || s.value >= s.max_value} aria-label="increase">+</button>
                          </span>
                        ) : (
                          <>{s.string_value || "—"}</>
                        )}
                        {s.changed && <small title={`Saved: ${s.last_saved}`}> (was {s.last_saved})</small>}
                      </dd>
                    </div>
                  );
                })}
              </dl>
            </section>
          ))}
        </div>
      )}

      {setup && setup.groups.length === 0 && !error && (
        <p className="muted">No setup settings reported for this car.</p>
      )}
    </div>
  );
}
