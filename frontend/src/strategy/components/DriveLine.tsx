// DriveLine draws the player's driven line for the last completed lap, top-down,
// from the world X/Z path the engine buffers. It's a plain auto-fitted SVG
// polyline — enough to see the shape of the line and spot where it's scruffy.
// (Comparing it against a reference/rival line comes in a later iteration.)
import { type Vec2 } from "../model";

const W = 520;
const H = 320;
const PAD = 16;

export default function DriveLine({ path }: { path: Vec2[] | undefined }) {
  if (!path || path.length < 2) {
    return <p className="muted">Complete a lap to see the driven line.</p>;
  }

  // Fit the world-space path into the viewport, preserving aspect ratio. World Z
  // is flipped so "north" is up (screen Y grows downward).
  const xs = path.map((p) => p.x);
  const zs = path.map((p) => p.z);
  const minX = Math.min(...xs), maxX = Math.max(...xs);
  const minZ = Math.min(...zs), maxZ = Math.max(...zs);
  const spanX = maxX - minX || 1;
  const spanZ = maxZ - minZ || 1;
  const scale = Math.min((W - 2 * PAD) / spanX, (H - 2 * PAD) / spanZ);
  const offX = (W - spanX * scale) / 2;
  const offZ = (H - spanZ * scale) / 2;

  const points = path
    .map((p) => {
      const x = offX + (p.x - minX) * scale;
      const y = offZ + (maxZ - p.z) * scale; // flip Z
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");

  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="strat-driveline" role="img" aria-label="Driven line, last lap">
      <polyline points={points} className="strat-driveline-path" />
    </svg>
  );
}
