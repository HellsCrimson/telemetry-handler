// DriverVs compares the player's car against a chosen live-session rival, so the
// strategist can see where the other car is using or saving its tires and fuel,
// and how the pace compares. The per-mini-sector data is each car's last
// COMPLETED lap (the engine accumulates it for every car), so the comparison is
// like-for-like once both have set a lap.
import { useState } from "react";
import { type SessionState, type CarState, playerCar, byRaceOrder, classColor, formatLapTime } from "../model";
import MiniSectorBars from "../components/MiniSectorBars";

export default function DriverVs({ state }: { state: SessionState }) {
  const player = playerCar(state);
  const others = byRaceOrder(state).filter((c) => c.id !== player?.id && c.place > 0);
  const [rivalId, setRivalId] = useState<number | null>(null);

  // Default the rival to the car directly ahead on track (first in race order
  // that isn't us), once we have a field.
  const rival: CarState | undefined =
    others.find((c) => c.id === rivalId) ?? others.find((c) => (player ? c.place < player.place : true)) ?? others[0];

  if (!player) return <p className="muted">Waiting for the player car…</p>;
  if (others.length === 0) return <p className="muted">No other cars in the session to compare against.</p>;

  return (
    <div className="strat-vs">
      <div className="strat-vs-pick">
        <label>
          Compare against:{" "}
          <select value={rival?.id ?? ""} onChange={(e) => setRivalId(Number(e.target.value))}>
            {others.map((c) => (
              <option key={c.id} value={c.id}>
                P{c.place} · {c.driver || c.car_name} ({c.class})
              </option>
            ))}
          </select>
        </label>
      </div>

      <section className="strat-group">
        <h3>Pace</h3>
        <div className="strat-vs-pace">
          <PaceCol label="You" car={player} />
          <PaceCol label={rival?.driver || rival?.car_name || "Rival"} car={rival} accent={classColor(rival?.class ?? "")} />
        </div>
      </section>

      <Comparison title="Tire usage by mini-sector (last lap)" you={player} rival={rival} pick={(m) => m.tire_wear.reduce((a, b) => a + b, 0)} format={(v) => `${(v * 100).toFixed(2)} %`} color="var(--red)" />
      <Comparison title="Fuel usage by mini-sector (last lap)" you={player} rival={rival} pick={(m) => m.fuel_used} format={(v) => `${v.toFixed(3)} L`} color="var(--blue)" />
    </div>
  );
}

function PaceCol({ label, car, accent }: { label: string; car?: CarState; accent?: string }) {
  return (
    <div className="strat-vs-col">
      <h4 style={accent ? { color: accent } : undefined}>{label}</h4>
      <div className="strat-readout"><span>Best</span><strong>{formatLapTime(car?.best_lap ?? 0)}</strong></div>
      <div className="strat-readout"><span>Last</span><strong>{formatLapTime(car?.last_lap ?? 0)}</strong></div>
      <div className="strat-readout"><span>Gap to leader</span><strong>{car && car.gap_to_leader > 0 ? `+${car.gap_to_leader.toFixed(1)}s` : "—"}</strong></div>
    </div>
  );
}

function Comparison({
  title,
  you,
  rival,
  pick,
  format,
  color,
}: {
  title: string;
  you: CarState;
  rival?: CarState;
  pick: (m: { tire_wear: number[]; fuel_used: number }) => number;
  format: (v: number) => string;
  color: string;
}) {
  const youVals = (you.mini_sectors ?? []).map(pick);
  const rivalVals = (rival?.mini_sectors ?? []).map(pick);
  return (
    <section className="strat-group">
      <h3>{title}</h3>
      {youVals.length === 0 ? (
        <p className="muted">You haven’t completed a lap yet.</p>
      ) : (
        <>
          <p className="strat-vs-label">You</p>
          <MiniSectorBars values={youVals} color={color} format={format} />
        </>
      )}
      {rivalVals.length === 0 ? (
        <p className="muted">Rival hasn’t completed a lap yet.</p>
      ) : (
        <>
          <p className="strat-vs-label">{rival?.driver || rival?.car_name || "Rival"}</p>
          <MiniSectorBars values={rivalVals} color={classColor(rival?.class ?? "")} format={format} />
        </>
      )}
    </section>
  );
}
