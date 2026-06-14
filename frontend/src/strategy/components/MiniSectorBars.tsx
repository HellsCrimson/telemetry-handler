// MiniSectorBars draws a barebones bar chart of one value per mini-sector. It's
// the workhorse of the "by corner / by straight" coaching views: each bar is one
// slice of the lap, so tall bars mark where a resource (tire, fuel) is spent. No
// charting library — a row of scaled divs is simpler and matches the brief.
export default function MiniSectorBars({
  values,
  color,
  format,
  highlight,
  corners,
}: {
  values: number[];
  color: string;
  format: (v: number) => string;
  // optional secondary series drawn as a faint line label under each bar (e.g. min speed)
  highlight?: (i: number) => string | undefined;
  // optional per-mini-sector corner label ("T1"/""=straight); when present, a
  // corner's bar is tinted so corners stand out from straights.
  corners?: string[];
}) {
  const max = Math.max(...values, 1e-9);
  return (
    <div className="strat-bars">
      {values.map((v, i) => {
        const corner = corners?.[i] ?? "";
        const label = corner || `${i + 1}`;
        const title = corner ? `${corner} (mini-sector ${i + 1})` : `Mini-sector ${i + 1}`;
        return (
          <div className={`strat-bar${corner ? " is-corner" : ""}`} key={i} title={`${title}: ${format(v)}`}>
            <div className="strat-bar-track">
              <div className="strat-bar-fill" style={{ height: `${(v / max) * 100}%`, background: color }} />
            </div>
            <span className="strat-bar-idx">{label}</span>
            {highlight && <span className="strat-bar-sub">{highlight(i)}</span>}
          </div>
        );
      })}
    </div>
  );
}
