// DriveLine draws one or more driven lines (world X/Z paths) top-down, auto-fitted
// to a shared frame so they overlay correctly. With one path it's just "your last
// lap"; with two it powers the comparisons — your last vs your best, or your line
// vs a rival's. The paths share bounds so the overlay is spatially honest.
//
// It supports scroll-to-zoom (toward the cursor) and drag-to-pan via a stateful
// SVG viewBox, with a reset, so big tracks can be inspected corner-by-corner.
import { useRef, useState } from "react";
import { type Vec2 } from "../model";

const W = 520;
const H = 320;
const PAD = 16;
const MIN_W = W / 12; // most-zoomed-in viewBox width

export type NamedPath = { points: Vec2[] | undefined; color: string; label: string };
type DrawnPath = { points: Vec2[]; color: string; label: string };
type ViewBox = { x: number; y: number; w: number; h: number };

const FULL: ViewBox = { x: 0, y: 0, w: W, h: H };

export default function DriveLine({ paths }: { paths: NamedPath[] }) {
  const usable: DrawnPath[] = paths.filter((p): p is DrawnPath => !!p.points && p.points.length >= 2);
  const svgRef = useRef<SVGSVGElement>(null);
  const drag = useRef<{ x: number; y: number } | null>(null);
  const [vb, setVb] = useState<ViewBox>(FULL);

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

  const onWheel = (e: React.WheelEvent<SVGSVGElement>) => {
    e.preventDefault();
    const rect = svgRef.current?.getBoundingClientRect();
    if (!rect) return;
    const mx = (e.clientX - rect.left) / rect.width; // 0..1 across the view
    const my = (e.clientY - rect.top) / rect.height;
    const factor = e.deltaY < 0 ? 0.85 : 1 / 0.85;
    let nw = Math.min(W, Math.max(MIN_W, vb.w * factor));
    const nh = nw * (H / W); // keep aspect
    // Keep the point under the cursor fixed.
    const px = vb.x + mx * vb.w;
    const py = vb.y + my * vb.h;
    setVb({ x: px - mx * nw, y: py - my * nh, w: nw, h: nh });
  };

  const onPointerDown = (e: React.PointerEvent<SVGSVGElement>) => {
    drag.current = { x: e.clientX, y: e.clientY };
    (e.target as Element).setPointerCapture?.(e.pointerId);
  };
  const onPointerMove = (e: React.PointerEvent<SVGSVGElement>) => {
    if (!drag.current) return;
    const rect = svgRef.current?.getBoundingClientRect();
    if (!rect) return;
    const dx = (e.clientX - drag.current.x) * (vb.w / rect.width);
    const dy = (e.clientY - drag.current.y) * (vb.h / rect.height);
    drag.current = { x: e.clientX, y: e.clientY };
    setVb((v) => ({ ...v, x: v.x - dx, y: v.y - dy }));
  };
  const onPointerUp = () => {
    drag.current = null;
  };

  const zoomed = vb.w < W - 0.5;

  return (
    <div>
      <svg
        ref={svgRef}
        viewBox={`${vb.x} ${vb.y} ${vb.w} ${vb.h}`}
        className="strat-driveline"
        role="img"
        aria-label="Driven line"
        onWheel={onWheel}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerLeave={onPointerUp}
      >
        {usable.map((p, i) => (
          <polyline
            key={i}
            points={toPoints(p.points)}
            fill="none"
            stroke={p.color}
            strokeWidth={2.5}
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
            opacity={i === 0 ? 1 : 0.6}
          />
        ))}
      </svg>
      <div className="strat-line-legend">
        {usable.map((p, i) => (
          <span key={i} className="strat-line-key">
            <span className="strat-line-swatch" style={{ background: p.color }} /> {p.label}
          </span>
        ))}
        {zoomed && (
          <button className="secondary strat-line-reset" onClick={() => setVb(FULL)}>
            Reset view
          </button>
        )}
      </div>
      <p className="muted strat-axis-note">Scroll to zoom, drag to pan.</p>
    </div>
  );
}
