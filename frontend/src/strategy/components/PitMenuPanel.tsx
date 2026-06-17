// PitMenuPanel mirrors what the driver currently has dialled into the in-game pit
// menu (REST /rest/garage/PitMenu/receivePitMenu, merged onto
// state.strategy.pit_menu): refuel amount, virtual-energy target, tyre choice,
// driver, repairs. The strategist uses it to confirm the car is set up to take on
// what the plan calls for before the driver commits to the stop. Rows with no
// current value (N/A) are hidden so it stays readable.
import { type PitMenuEntry } from "../model";

// label strips the trailing colon LMU includes in the menu names ("DRIVER:").
function label(name: string): string {
  return name.replace(/:\s*$/, "");
}

export default function PitMenuPanel({ menu }: { menu: PitMenuEntry[] }) {
  const rows = (menu ?? []).filter((m) => m.current && m.current !== "N/A");
  if (rows.length === 0) return null;

  return (
    <section className="strat-group strat-pitmenu">
      <h3>Pit menu</h3>
      <dl className="strat-pitmenu-list">
        {rows.map((m) => (
          <div key={m.name} className="strat-pitmenu-row">
            <dt>{label(m.name)}</dt>
            <dd>{m.current}</dd>
          </div>
        ))}
      </dl>
      <p className="muted strat-axis-note">What the driver currently has set for the next stop.</p>
    </section>
  );
}
