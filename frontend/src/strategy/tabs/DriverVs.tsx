// DriverVs compares the player's car against a chosen live-session rival: pace,
// per-mini-sector tire/fuel use, and the driven line. The comparison uses each
// car's BEST completed lap when available (steadier than the last lap). Picking a
// rival tells the engine to start buffering that car's driven line (the player's
// is always buffered), which powers the line overlay once the rival sets a lap.
import { useEffect, useState } from "react";
import { Service } from "../../../bindings/telemetry-handler/app";
import { type SessionState, type CarState, type MiniSectorState, playerCar, byRaceOrder, classColor, formatLapTime } from "../model";
import MiniSectorBars from "../components/MiniSectorBars";
import DriveLine from "../components/DriveLine";

// refLap prefers a car's best lap, falling back to its last completed lap.
function refLap(car: CarState | undefined): MiniSectorState[] {
  return car?.best_sectors ?? car?.mini_sectors ?? [];
}

export default function DriverVs({ state }: { state: SessionState }) {
  const player = playerCar(state);
  const others = byRaceOrder(state).filter((c) => c.id !== player?.id && c.place > 0);
  const [rivalId, setRivalId] = useState<number | null>(null);

  const rival: CarState | undefined =
    others.find((c) => c.id === rivalId) ?? others.find((c) => (player ? c.place < player.place : true)) ?? others[0];

  // Tell the engine which rival's line to buffer. Cleared on unmount.
  useEffect(() => {
    if (rival) Service.SetComparisonCar(rival.id);
    return () => {
      Service.SetComparisonCar(-1);
    };
  }, [rival?.id]);

  if (!player) return <p className="muted">Waiting for the player car…</p>;
  if (others.length === 0) return <p className="muted">No other cars in the session to compare against.</p>;

  const youRef = refLap(player);
  const rivalRef = refLap(rival);

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

      <Comparison title="Tire usage by mini-sector (best lap)" you={youRef} rival={rivalRef} rivalColor={classColor(rival?.class ?? "")} rivalLabel={rival?.driver || rival?.car_name || "Rival"} pick={(m) => m.tire_wear.reduce((a, b) => a + b, 0)} format={(v) => `${(v * 100).toFixed(2)} %`} youColor="var(--red)" />
      <Comparison title="Fuel usage by mini-sector (best lap)" you={youRef} rival={rivalRef} rivalColor={classColor(rival?.class ?? "")} rivalLabel={rival?.driver || rival?.car_name || "Rival"} pick={(m) => m.fuel_used} format={(v) => `${v.toFixed(3)} L`} youColor="var(--blue)" />

      <section className="strat-group">
        <h3>Driven line — you vs {rival?.driver || rival?.car_name || "rival"}</h3>
        <DriveLine
          paths={[
            { points: player.best_path ?? player.lap_path, color: "var(--green)", label: "You" },
            { points: rival?.best_path ?? rival?.lap_path, color: classColor(rival?.class ?? ""), label: rival?.driver || "Rival" },
          ]}
        />
        <p className="muted strat-axis-note">The rival’s line starts recording when you select them — give it a lap to appear.</p>
      </section>
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
  youColor,
  rivalColor,
  rivalLabel,
  pick,
  format,
}: {
  title: string;
  you: MiniSectorState[];
  rival: MiniSectorState[];
  youColor: string;
  rivalColor: string;
  rivalLabel: string;
  pick: (m: MiniSectorState) => number;
  format: (v: number) => string;
}) {
  return (
    <section className="strat-group">
      <h3>{title}</h3>
      {you.length === 0 ? (
        <p className="muted">You haven’t set a lap yet.</p>
      ) : (
        <>
          <p className="strat-vs-label">You</p>
          <MiniSectorBars values={you.map(pick)} color={youColor} format={format} />
        </>
      )}
      {rival.length === 0 ? (
        <p className="muted">{rivalLabel} hasn’t set a lap yet.</p>
      ) : (
        <>
          <p className="strat-vs-label">{rivalLabel}</p>
          <MiniSectorBars values={rival.map(pick)} color={rivalColor} format={format} />
        </>
      )}
    </section>
  );
}
