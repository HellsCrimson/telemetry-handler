// WeatherPanel summarises the session weather for strategy calls: temperatures,
// rain and wind, plus the 3x3 rain grid (LMU reports rain intensity across nine
// zones of the track, so you can see a shower arriving on one part of the
// circuit before it reaches the rest).
import { type WeatherState } from "../model";

export default function WeatherPanel({ weather }: { weather: WeatherState }) {
  const grid = weather.rain_grid ?? [];
  return (
    <section className="strat-group strat-weather">
      <h3>Weather</h3>
      <div className="strat-readouts">
        <Readout label="Air" value={weather.ambient_temp.toFixed(0)} unit="°C" />
        <Readout label="Track" value={weather.track_temp.toFixed(0)} unit="°C" />
        <Readout label="Rain" value={(weather.raining * 100).toFixed(0)} unit="%" />
        <Readout label="Cloud" value={(weather.cloudiness * 100).toFixed(0)} unit="%" />
        <Readout label="Wind" value={weather.wind_max.toFixed(0)} unit="m/s" />
      </div>
      {grid.length === 9 && grid.some((v) => v > 0) && (
        <>
          <div className="strat-raingrid" aria-label="Rain intensity across the track">
            {grid.map((v, i) => (
              <span key={i} className="strat-raincell" style={{ background: rainColor(v) }} title={`Zone ${i + 1}: ${(v * 100).toFixed(0)}% rain`} />
            ))}
          </div>
          <p className="muted strat-axis-note">
            Rain intensity sampled at a 3×3 grid of points across the track — darker = wetter. Lets you spot a shower
            sitting on one part of the circuit before it spreads.
          </p>
        </>
      )}
    </section>
  );
}

// rainColor fades from the dark panel colour (dry) to blue (heavy rain).
function rainColor(v: number): string {
  const a = Math.min(1, Math.max(0, v));
  return `rgba(74, 163, 255, ${a.toFixed(2)})`;
}

function Readout({ label, value, unit }: { label: string; value: string; unit?: string }) {
  return (
    <div className="strat-readout">
      <span>{label}</span>
      <strong>{value}{unit ? <small> {unit}</small> : null}</strong>
    </div>
  );
}
