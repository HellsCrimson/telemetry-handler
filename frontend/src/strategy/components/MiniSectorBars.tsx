// MiniSectorBars draws a barebones bar chart of one value per mini-sector. It's
// the workhorse of the "by corner / by straight" coaching views: each bar is one
// slice of the lap, so tall bars mark where a resource (tire, fuel) is spent. No
// charting library — a row of scaled divs is simpler and matches the brief.
export default function MiniSectorBars({
  values,
  color,
  format,
  highlight,
}: {
  values: number[];
  color: string;
  format: (v: number) => string;
  // optional secondary series drawn as a faint line label under each bar (e.g. min speed)
  highlight?: (i: number) => string | undefined;
}) {
  const max = Math.max(...values, 1e-9);
  return (
    <div className="strat-bars">
      {values.map((v, i) => (
        <div className="strat-bar" key={i} title={`Mini-sector ${i + 1}: ${format(v)}`}>
          <div className="strat-bar-track">
            <div className="strat-bar-fill" style={{ height: `${(v / max) * 100}%`, background: color }} />
          </div>
          <span className="strat-bar-idx">{i + 1}</span>
          {highlight && <span className="strat-bar-sub">{highlight(i)}</span>}
        </div>
      ))}
    </div>
  );
}
