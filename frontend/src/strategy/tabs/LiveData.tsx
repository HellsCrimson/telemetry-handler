// LiveData is the "Main" strategy tab: the essential data points for the player's
// car at a glance — fuel/energy, tires, lap times. Phase 1 shows the instantaneous
// state straight from the latest frame; the per-lap/per-corner statistics and
// usage rates arrive once the accumulation engine lands (Phase 2).
import { type SessionState, type CarState, playerCar, formatLapTime, avgTemp } from "../model";

// CORNER_LABELS name the four wheels in the [FL, FR, RL, RR] order the model uses.
const CORNER_LABELS = ["FL", "FR", "RL", "RR"];

export default function LiveData({ state }: { state: SessionState }) {
  const player = playerCar(state);
  if (!player) return <p className="muted">Waiting for the player car…</p>;

  return (
    <div className="strat-livedata">
      <section className="strat-group">
        <h3>Energy / Fuel</h3>
        <div className="strat-readouts">
          <Readout label="Fuel" value={player.fuel.toFixed(1)} unit="L" />
          <Readout label="Tank" value={player.fuel_capacity > 0 ? `${((player.fuel / player.fuel_capacity) * 100).toFixed(0)}` : "—"} unit="%" />
          <Readout label="Battery" value={(player.battery * 100).toFixed(0)} unit="%" />
        </div>
      </section>

      <section className="strat-group">
        <h3>Time / AVG</h3>
        <div className="strat-readouts">
          <Readout label="Last lap" value={formatLapTime(player.last_lap)} />
          <Readout label="Best lap" value={formatLapTime(player.best_lap)} />
          <Readout label="Lap" value={`${player.total_laps}`} />
          <Readout label="Position" value={player.place ? `P${player.place}` : "—"} />
        </div>
      </section>

      <section className="strat-group">
        <h3>Tires</h3>
        <div className="strat-tires">
          {player.tires.map((tire, i) => (
            <div className="strat-tire" key={i}>
              <span className="strat-tire-corner">{CORNER_LABELS[i]}</span>
              <strong>{avgTemp(tire).toFixed(0)}°C</strong>
              <small>{tire.compound || "—"}</small>
              <small>{(tire.wear * 100).toFixed(0)}% · {tire.pressure.toFixed(0)} kPa</small>
            </div>
          ))}
        </div>
      </section>

      <FieldSummary state={state} player={player} />
    </div>
  );
}

function FieldSummary({ state, player }: { state: SessionState; player: CarState }) {
  return (
    <section className="strat-group">
      <h3>Field</h3>
      <div className="strat-readouts">
        <Readout label="Cars" value={`${state.cars.length}`} />
        <Readout label="Gap ahead" value={player.gap_to_next > 0 ? `+${player.gap_to_next.toFixed(1)}s` : "—"} />
        <Readout label="Gap leader" value={player.gap_to_leader > 0 ? `+${player.gap_to_leader.toFixed(1)}s` : "—"} />
        <Readout label="Track temp" value={state.weather.track_temp.toFixed(0)} unit="°C" />
      </div>
    </section>
  );
}

function Readout({ label, value, unit }: { label: string; value: string; unit?: string }) {
  return (
    <div className="strat-readout">
      <span>{label}</span>
      <strong>{value}{unit ? <small> {unit}</small> : null}</strong>
    </div>
  );
}
