// DriverCoaching shows where the driver is spending tire and fuel around the lap,
// broken down by mini-sector, so the strategist can say "you're using too much
// tire into that corner" or "lift earlier on this straight to save fuel". The
// data is the last COMPLETED lap from the live accumulation engine
// (engineer/lapaccum.go); the in-progress lap fills in as the current lap runs.
import { type SessionState, playerCar } from "../model";
import MiniSectorBars from "../components/MiniSectorBars";
import DriveLine from "../components/DriveLine";

export default function DriverCoaching({ state }: { state: SessionState }) {
  const player = playerCar(state);
  const lap = player?.mini_sectors;
  const best = player?.best_sectors;
  const live = player?.lap_in_progress;

  if (!player) return <p className="muted">Waiting for the player car…</p>;
  if (!lap || lap.length === 0) {
    return <p className="muted">Complete a full lap to see the per-corner breakdown. (Lap in progress…)</p>;
  }

  // Total tire wear consumed per mini-sector = sum across the four wheels.
  const wearPerSector = lap.map((m) => m.tire_wear.reduce((a, b) => a + b, 0));
  const fuelPerSector = lap.map((m) => m.fuel_used);
  const minSpeed = (i: number) => `${(lap[i].min_speed * 3.6).toFixed(0)}`;

  const totalWear = wearPerSector.reduce((a, b) => a + b, 0);
  const totalFuel = fuelPerSector.reduce((a, b) => a + b, 0);
  const bestFuel = best ? best.reduce((a, m) => a + m.fuel_used, 0) : 0;

  return (
    <div className="strat-coaching">
      <section className="strat-group">
        <h3>Tire usage by mini-sector — last lap (total {(totalWear * 100).toFixed(1)}%)</h3>
        <MiniSectorBars values={wearPerSector} color="var(--red)" format={(v) => `${(v * 100).toFixed(2)} %`} highlight={minSpeed} />
        <p className="muted strat-axis-note">Bar = tire wear consumed (4 wheels). Number under each bar = min speed (km/h).</p>
      </section>

      <section className="strat-group">
        <h3>
          Fuel usage by mini-sector — last lap (total {totalFuel.toFixed(2)} L
          {bestFuel > 0 ? ` · best lap ${bestFuel.toFixed(2)} L` : ""})
        </h3>
        <MiniSectorBars values={fuelPerSector} color="var(--blue)" format={(v) => `${v.toFixed(3)} L`} highlight={minSpeed} />
        <p className="muted strat-axis-note">Bar = fuel burned. Lift-and-coast zones show as shorter bars on the straights.</p>
      </section>

      <section className="strat-group">
        <h3>Driven line — last lap vs your best lap</h3>
        <DriveLine
          paths={[
            { points: player.lap_path, color: "var(--green)", label: "Last lap" },
            { points: player.best_path, color: "var(--blue)", label: "Best lap" },
          ]}
        />
      </section>

      {live && live.some((m) => m.time_spent > 0) && (
        <p className="muted">Current lap is being recorded — the charts above update when it completes.</p>
      )}
    </div>
  );
}
