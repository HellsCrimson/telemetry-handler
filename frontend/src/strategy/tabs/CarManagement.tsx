// CarManagement recaps the player car's systems: powertrain temps, hybrid/energy,
// aero & suspension, driver aids and damage. Everything here is instantaneous
// (straight from the latest frame's PlayerDetail), so no engine accumulation is
// needed. The static setup values (ARB stiffness, springs, gearing, wings) are NOT
// in the telemetry frame — they are read separately from LMU's REST API and shown
// on the dedicated Setup tab.
import { type SessionState, playerCar, avgTemp } from "../model";

const ELECTRIC_STATE = ["Unavailable", "Inactive", "Propulsion", "Regen"];
const AID_LEVEL = (n: number) => (n <= 0 ? "Off" : String(n));
const CORNERS = ["FL", "FR", "RL", "RR"];

export default function CarManagement({ state }: { state: SessionState }) {
  const d = state.player;
  const car = playerCar(state);
  if (!d?.present || !car) return <p className="muted">Waiting for the player car…</p>;

  return (
    <div className="strat-livedata">
      <section className="strat-group">
        <h3>Powertrain</h3>
        <div className="strat-readouts">
          <Readout label="RPM" value={d.rpm.toFixed(0)} unit={`/ ${d.max_rpm.toFixed(0)}`} />
          <Readout label="Water" value={d.water_temp.toFixed(0)} unit="°C" />
          <Readout label="Oil" value={d.oil_temp.toFixed(0)} unit="°C" />
        </div>
      </section>

      <section className="strat-group">
        <h3>Energy / Hybrid</h3>
        <div className="strat-readouts">
          <Readout label="Battery" value={(car.battery * 100).toFixed(0)} unit="%" />
          <Readout label="Motor" value={ELECTRIC_STATE[d.electric_state] ?? "—"} />
          <Readout label="Motor temp" value={d.electric_temp > 0 ? d.electric_temp.toFixed(0) : "—"} unit="°C" />
        </div>
      </section>

      <section className="strat-group">
        <h3>Aero / Suspension</h3>
        <div className="strat-readouts">
          <Readout label="Downforce F" value={(d.front_downforce / 1000).toFixed(1)} unit="kN" />
          <Readout label="Downforce R" value={(d.rear_downforce / 1000).toFixed(1)} unit="kN" />
          <Readout label="Drag" value={(d.drag / 1000).toFixed(1)} unit="kN" />
          <Readout label="Ride F" value={(d.front_ride_height * 1000).toFixed(0)} unit="mm" />
          <Readout label="Ride R" value={(d.rear_ride_height * 1000).toFixed(0)} unit="mm" />
        </div>
      </section>

      <section className="strat-group">
        <h3>Driver aids</h3>
        <div className="strat-readouts">
          <Readout label="Brake bias" value={`${(d.rear_brake_bias * 100).toFixed(1)}`} unit="% R" />
          <Readout label="TC" value={AID_LEVEL(d.traction_control)} />
          <Readout label="ABS" value={AID_LEVEL(d.abs)} />
          <Readout label="Stability" value={AID_LEVEL(d.stability_control)} />
          <Readout label="Pit limit" value={(d.pit_speed_limit * 3.6).toFixed(0)} unit="km/h" />
        </div>
      </section>

      <section className="strat-group">
        <h3>Damage {d.worst_dent > 0 ? `· level ${d.worst_dent}` : "· none"}</h3>
        <div className="strat-dents">
          {d.dent_severity.map((sev, i) => (
            <span key={i} className={`strat-dent strat-dent-${sev}`} title={`Panel ${i + 1}: ${["none", "minor", "major"][sev] ?? sev}`} />
          ))}
        </div>
        <p className="muted strat-axis-note">Each block is a body panel; amber = minor, red = major. Static setup values are on the Setup tab.</p>
      </section>

      <section className="strat-group">
        <h3>Tires</h3>
        <div className="strat-tires">
          {car.tires.map((tire, i) => (
            <div className="strat-tire" key={i}>
              <span className="strat-tire-corner">{CORNERS[i]}</span>
              <strong>{avgTemp(tire).toFixed(0)}°C</strong>
              <small>brake {tire.brake_temp.toFixed(0)}°C</small>
              <small>{(tire.wear * 100).toFixed(0)}% · {tire.pressure.toFixed(0)} kPa</small>
            </div>
          ))}
        </div>
      </section>
    </div>
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
