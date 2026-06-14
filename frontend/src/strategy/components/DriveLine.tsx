// DriveLine draws one or more driven lines (world X/Z paths) top-down, auto-fitted
// to a shared frame so they overlay correctly. With one path it's just "your last
// lap"; with two it powers the comparisons — your last vs your best, or your line
// vs a rival's. The paths share bounds so the overlay is spatially honest.
import { type Vec2 } from "../model";

const W = 520;
const H = 320;
const PAD = 16;

export type NamedPath = { points: Vec2[] | undefined; color: string; label: string };

type DrawnPath = { points: Vec2[]; color: string; label: string };

export default function DriveLine({ paths }: { paths: NamedPath[] }) {
  const usable: DrawnPath[] = paths.filter((p): p is DrawnPath => !!p.points && p.points.length >= 2);
  if (usable.length === 0) {
    return <p className="muted">Complete a lap to see the driven line.</p>;
  }

  // Shared bounds across all paths so overlaid lines line up.
  const all = usable.flatMap((p) => p.points);
  const xs = all.map((p) => p.x);
  const zs = all.map((p) => p.z);
  const minX = Math.min(...xs), maxX = Math.max(...xs);
  const minZ = Math.min(...zs), maxZ = Math.max(...zs);
  const spanX = maxX - minX || 1;
  const spanZ = maxZ - minZ || 1;
  const scale = Math.min((W - 2 * PAD) / spanX, (H - 2 * PAD) / spanZ);
  const offX = (W - spanX * scale) / 2;
  const offZ = (H - spanZ * scale) / 2;

  const toPoints = (pts: Vec2[]) =>
    pts
      .map((p) => {
        const x = offX + (p.x - minX) * scale;
        const y = offZ + (maxZ - p.z) * scale; // flip Z so north is up
        return `${x.toFixed(1)},${y.toFixed(1)}`;
      })
      .join(" ");

  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} className="strat-driveline" role="img" aria-label="Driven line">
        {usable.map((p, i) => (
          <polyline key={i} points={toPoints(p.points)} fill="none" stroke={p.color} strokeWidth={2.5} strokeLinejoin="round" strokeLinecap="round" opacity={i === 0 ? 1 : 0.6} />
        ))}
      </svg>
      <div className="strat-line-legend">
        {usable.map((p, i) => (
          <span key={i} className="strat-line-key">
            <span className="strat-line-swatch" style={{ background: p.color }} /> {p.label}
          </span>
        ))}
      </div>
    </div>
  );
}
