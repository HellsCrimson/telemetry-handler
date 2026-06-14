// PitParameters turns the measured fuel/energy use into a pit call: how much fuel
// to put in to reach the end, given the laps remaining and the consumption from
// the last completed lap. A small safety margin (one lap) is added so the driver
// never runs dry. Energy (hybrid) is shown for awareness; it self-manages.
import { type SessionState, playerCar, lapTotal, lapsRemaining } from "../model";

const SAFETY_LAPS = 1;

export default function PitParameters({ state }: { state: SessionState }) {
  const player = playerCar(state);
  if (!player) return null;

  const fuelPerLap = lapTotal(player.mini_sectors, (m) => m.fuel_used);
  const lapsLeft = lapsRemaining(state, player);
  const haveData = fuelPerLap > 0 && lapsLeft > 0;

  const fuelToFinish = fuelPerLap * (lapsLeft + SAFETY_LAPS);
  const fuelToAdd = Math.max(0, fuelToFinish - player.fuel);
  const overTank = player.fuel_capacity > 0 && fuelToAdd > player.fuel_capacity - player.fuel;

  return (
    <section className="strat-group strat-pit">
      <h3>Pit parameters</h3>
      {!haveData ? (
        <p className="muted">Need a completed lap and a known race length to compute fuel. (Lap-limited or timed race only.)</p>
      ) : (
        <div className="strat-readouts">
          <Readout label="Fuel / lap" value={fuelPerLap.toFixed(2)} unit="L" />
          <Readout label="Laps left" value={`${lapsLeft}`} unit={`+${SAFETY_LAPS}`} />
          <Readout label="In tank" value={player.fuel.toFixed(1)} unit="L" />
          <Readout label="Add" value={fuelToAdd.toFixed(1)} unit="L" warn={overTank} />
        </div>
      )}
      {overTank && <p className="muted strat-axis-note">⚠ Exceeds tank capacity — a second stop or fuel saving is needed.</p>}
    </section>
  );
}

function Readout({ label, value, unit, warn }: { label: string; value: string; unit?: string; warn?: boolean }) {
  return (
    <div className="strat-readout">
      <span>{label}</span>
      <strong style={warn ? { color: "var(--red)" } : undefined}>{value}{unit ? <small> {unit}</small> : null}</strong>
    </div>
  );
}
