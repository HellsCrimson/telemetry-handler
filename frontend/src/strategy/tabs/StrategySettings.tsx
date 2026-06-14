// StrategySettings edits the strategy preferences kept in the browser: the
// pit-loss assumption (used by the pit-window, pit-parameters and undercut tools)
// and the fuel safety margin. Changes persist via useSettings and apply live.
import { type StrategySettings as Settings } from "../useSettings";

export default function StrategySettings({ settings, update }: { settings: Settings; update: (patch: Partial<Settings>) => void }) {
  return (
    <div className="strat-livedata">
      <section className="strat-group">
        <h3>Pit strategy</h3>
        <label className="strat-field">
          <span>Pit-loss (seconds)</span>
          <input
            type="number"
            min={0}
            step={1}
            value={settings.pitLossSeconds}
            onChange={(e) => update({ pitLossSeconds: Number(e.target.value) })}
          />
          <small className="muted">Time a full pit stop costs vs staying out. Drives the pit-window estimate and undercut maths.</small>
        </label>
        <label className="strat-field">
          <span>Fuel safety margin (laps)</span>
          <input
            type="number"
            min={0}
            step={1}
            value={settings.safetyLaps}
            onChange={(e) => update({ safetyLaps: Number(e.target.value) })}
          />
          <small className="muted">Extra laps of fuel added to the “fuel to add” pit call.</small>
        </label>
      </section>

      <section className="strat-group">
        <h3>About</h3>
        <p className="muted">
          These settings are stored in this browser. Game/IP/recording settings live in the Dashboard’s Settings tab.
        </p>
      </section>
    </div>
  );
}
