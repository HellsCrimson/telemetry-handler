// PitStopEstimate shows LMU's own projected pit-stop duration, broken down by
// activity (refuel, tyres, virtual-energy recharge, repairs, driver swap,
// penalties). It comes from the REST API (/rest/strategy/pitstop-estimate),
// merged onto the session state as state.strategy — the shared-memory frame has
// no equivalent. This is the time the strategist budgets against the pit-loss
// figure when deciding when to stop. Only the non-zero components are listed so a
// clean stop reads cleanly.
import { type StrategyState } from "../model";

// COMPONENTS lists the breakdown fields in display order with their labels.
const COMPONENTS: [keyof StrategyState["pit_estimate"], string][] = [
  ["fuel", "Refuel"],
  ["ve", "Energy"],
  ["tires", "Tyres"],
  ["damage", "Repairs"],
  ["driver_swap", "Driver swap"],
  ["penalties", "Penalties"],
];

export default function PitStopEstimate({ strategy }: { strategy: StrategyState }) {
  if (!strategy?.present) return null;
  const est = strategy.pit_estimate;
  const parts = COMPONENTS.filter(([k]) => est[k] > 0);

  return (
    <section className="strat-group strat-pitstop">
      <h3>Pit-stop time</h3>
      <div className="strat-readouts">
        <Readout label="Total" value={est.total.toFixed(1)} unit="s" big />
      </div>
      {parts.length > 0 ? (
        <ul className="strat-pitstop-breakdown">
          {parts.map(([k, label]) => (
            <li key={k}>
              <span>{label}</span>
              <strong>{est[k].toFixed(1)}<small> s</small></strong>
            </li>
          ))}
        </ul>
      ) : (
        <p className="muted strat-axis-note">No service required — a stop now costs only the pit-lane transit.</p>
      )}
      <p className="muted strat-axis-note">Service time only (LMU estimate); add your pit-lane loss for the full stop cost.</p>
    </section>
  );
}

function Readout({ label, value, unit, big }: { label: string; value: string; unit?: string; big?: boolean }) {
  return (
    <div className="strat-readout">
      <span>{label}</span>
      <strong style={big ? { fontSize: "28px" } : undefined}>{value}{unit ? <small> {unit}</small> : null}</strong>
    </div>
  );
}
