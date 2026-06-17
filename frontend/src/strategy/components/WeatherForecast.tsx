// WeatherForecast renders LMU's predicted weather across the session, node by
// node (REST /rest/sessions/weather, merged onto state.strategy.forecast). Unlike
// the live WeatherPanel — which shows conditions right now — this is the forecast
// a strategist reads to time a tyre call: a rising rain chance toward the finish
// is the cue to plan a wet stop. Nodes are ordered START → mid → FINISH.
import { type ForecastPoint } from "../model";

// nodeRank orders the forecast nodes along the session: START first, the NODE_NN
// percentage markers by their number, FINISH last.
function nodeRank(node: string): number {
  const n = node.toUpperCase();
  if (n === "START") return 0;
  if (n === "FINISH") return 101;
  const m = n.match(/(\d+)/);
  return m ? Number(m[1]) : 50;
}

// prettyNode turns "NODE_25" into "25%", leaving START/FINISH as words.
function prettyNode(node: string): string {
  const m = node.match(/(\d+)/);
  return m ? `${m[1]}%` : node.charAt(0) + node.slice(1).toLowerCase();
}

export default function WeatherForecast({ forecast }: { forecast: ForecastPoint[] }) {
  if (!forecast || forecast.length === 0) return null;

  // Group by session phase (PRACTICE/RACE/…); usually one is present.
  const bySession = new Map<string, ForecastPoint[]>();
  for (const p of forecast) {
    const arr = bySession.get(p.session) ?? [];
    arr.push(p);
    bySession.set(p.session, arr);
  }

  return (
    <section className="strat-group strat-forecast">
      <h3>Weather forecast</h3>
      {[...bySession.entries()].map(([session, points]) => {
        const rows = [...points].sort((a, b) => nodeRank(a.node) - nodeRank(b.node));
        return (
          <div key={session} className="strat-forecast-session">
            <div className="strat-forecast-title">{session.charAt(0) + session.slice(1).toLowerCase()}</div>
            <table className="strat-forecast-table">
              <thead>
                <tr><th>When</th><th>Sky</th><th>Rain</th><th>Air</th></tr>
              </thead>
              <tbody>
                {rows.map((p) => (
                  <tr key={p.node}>
                    <td>{prettyNode(p.node)}</td>
                    <td>{p.sky || "—"}</td>
                    <td className={p.rain_chance >= 30 ? "strat-forecast-wet" : undefined}>{p.rain_chance.toFixed(0)}%</td>
                    <td>{p.temperature.toFixed(0)}°</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
      })}
    </section>
  );
}
