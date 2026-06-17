import { useRef, useState } from "react";

export type CurvePoint = { x: number; y: number };

// monotoneTangents / evalCurve mirror the Go implementation in
// wheelbase/moza/curve.go exactly, so the editor preview matches what the wheel
// actually does. Monotone cubic (Fritsch–Carlson): smooth, no overshoot.
function monotoneTangents(p: CurvePoint[]): number[] {
  const n = p.length;
  const secant: number[] = new Array(n - 1);
  for (let i = 0; i < n - 1; i++) {
    const dx = p[i + 1].x - p[i].x;
    secant[i] = dx <= 0 ? 0 : (p[i + 1].y - p[i].y) / dx;
  }
  const m: number[] = new Array(n);
  m[0] = secant[0];
  m[n - 1] = secant[n - 2];
  for (let i = 1; i < n - 1; i++) {
    m[i] = secant[i - 1] * secant[i] <= 0 ? 0 : (secant[i - 1] + secant[i]) / 2;
  }
  for (let i = 0; i < n - 1; i++) {
    if (secant[i] === 0) {
      m[i] = 0;
      m[i + 1] = 0;
      continue;
    }
    const a = m[i] / secant[i];
    const b = m[i + 1] / secant[i];
    const s = a * a + b * b;
    if (s > 9) {
      const t = 3 / Math.sqrt(s);
      m[i] = t * a * secant[i];
      m[i + 1] = t * b * secant[i];
    }
  }
  return m;
}

const clamp01 = (v: number) => (v < 0 ? 0 : v > 1 ? 1 : v);

export function evalCurve(p: CurvePoint[], x: number): number {
  const n = p.length;
  if (n < 2) return clamp01(x);
  if (x <= p[0].x) return clamp01(p[0].y);
  if (x >= p[n - 1].x) return clamp01(p[n - 1].y);
  let i = 0;
  while (i < n - 2 && x > p[i + 1].x) i++;
  const m = monotoneTangents(p);
  const h = p[i + 1].x - p[i].x;
  const t = (x - p[i].x) / h;
  const t2 = t * t;
  const t3 = t2 * t;
  const h00 = 2 * t3 - 3 * t2 + 1;
  const h10 = t3 - 2 * t2 + t;
  const h01 = -2 * t3 + 3 * t2;
  const h11 = t3 - t2;
  return clamp01(h00 * p[i].y + h10 * h * m[i] + h01 * p[i + 1].y + h11 * h * m[i + 1]);
}

// presetCurve returns control points for a named quick-setup. These only seed
// the editor; the user is free to drag from there.
export function presetCurve(name: string): CurvePoint[] {
  switch (name) {
    case "exponential": // green holds longer, red squeezed near the top
      return [0, 0.25, 0.5, 0.75, 1].map((x) => ({ x, y: round(Math.pow(x, 2.5)) }));
    case "logarithmic": // bar fills early
      return [0, 0.25, 0.5, 0.75, 1].map((x) => ({ x, y: round(Math.pow(x, 1 / 2.5)) }));
    case "scurve":
      return [
        { x: 0, y: 0 },
        { x: 0.3, y: 0.12 },
        { x: 0.7, y: 0.88 },
        { x: 1, y: 1 },
      ];
    default: // linear
      return [
        { x: 0, y: 0 },
        { x: 1, y: 1 },
      ];
  }
}

const round = (v: number) => Math.round(v * 1000) / 1000;

const W = 320;
const H = 240;
const PAD = { l: 30, r: 12, t: 12, b: 24 };
const PLOT_W = W - PAD.l - PAD.r;
const PLOT_H = H - PAD.t - PAD.b;
const HIT_RADIUS = 14;
const MIN_GAP = 0.02;

const toPx = (x: number) => PAD.l + x * PLOT_W;
const toPy = (y: number) => PAD.t + (1 - y) * PLOT_H;

export function CurveEditor({
  points,
  onChange,
  colors,
}: {
  points: CurvePoint[];
  onChange: (pts: CurvePoint[]) => void;
  colors?: number[][];
}) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [drag, setDrag] = useState<number | null>(null);
  const pts = points.length >= 2 ? points : presetCurve("linear");

  // Convert a pointer event to data coordinates clamped to [0,1].
  function toData(e: React.PointerEvent | React.MouseEvent) {
    const rect = svgRef.current!.getBoundingClientRect();
    const sx = (e.clientX - rect.left) * (W / rect.width);
    const sy = (e.clientY - rect.top) * (H / rect.height);
    return {
      x: clamp01((sx - PAD.l) / PLOT_W),
      y: clamp01(1 - (sy - PAD.t) / PLOT_H),
    };
  }

  function hitTest(e: React.PointerEvent) {
    const rect = svgRef.current!.getBoundingClientRect();
    const sx = (e.clientX - rect.left) * (W / rect.width);
    const sy = (e.clientY - rect.top) * (H / rect.height);
    for (let i = 0; i < pts.length; i++) {
      const dx = sx - toPx(pts[i].x);
      const dy = sy - toPy(pts[i].y);
      if (dx * dx + dy * dy <= HIT_RADIUS * HIT_RADIUS) return i;
    }
    return -1;
  }

  function onPointerDown(e: React.PointerEvent) {
    e.preventDefault();
    svgRef.current!.setPointerCapture(e.pointerId);
    const hit = hitTest(e);
    if (hit >= 0) {
      setDrag(hit);
      return;
    }
    // Add a new interior point at the cursor and start dragging it.
    const d = toData(e);
    const next = [...pts];
    let idx = next.findIndex((p) => p.x > d.x);
    if (idx < 0) idx = next.length;
    next.splice(idx, 0, { x: round(d.x), y: round(d.y) });
    setDrag(idx);
    onChange(clampPoints(next, idx));
  }

  function onPointerMove(e: React.PointerEvent) {
    if (drag === null) return;
    const d = toData(e);
    const next = pts.map((p) => ({ ...p }));
    next[drag] = { x: round(d.x), y: round(d.y) };
    onChange(clampPoints(next, drag));
  }

  function onPointerUp(e: React.PointerEvent) {
    if (drag !== null) svgRef.current!.releasePointerCapture(e.pointerId);
    setDrag(null);
  }

  // Remove a point on double-click (endpoints stay; keep at least two points).
  function onDoubleClick(e: React.MouseEvent) {
    const rect = svgRef.current!.getBoundingClientRect();
    const sx = (e.clientX - rect.left) * (W / rect.width);
    const sy = (e.clientY - rect.top) * (H / rect.height);
    for (let i = 1; i < pts.length - 1; i++) {
      const dx = sx - toPx(pts[i].x);
      const dy = sy - toPy(pts[i].y);
      if (dx * dx + dy * dy <= HIT_RADIUS * HIT_RADIUS) {
        onChange(pts.filter((_, j) => j !== i));
        return;
      }
    }
  }

  // Sampled spline path.
  let path = "";
  for (let i = 0; i <= 64; i++) {
    const x = i / 64;
    const y = evalCurve(pts, x);
    path += `${i === 0 ? "M" : "L"}${toPx(x).toFixed(1)},${toPy(y).toFixed(1)} `;
  }

  const led = colors && colors.length ? colors : null;

  return (
    <svg
      ref={svgRef}
      className="curve-editor"
      viewBox={`0 0 ${W} ${H}`}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onDoubleClick={onDoubleClick}
    >
      <rect x={PAD.l} y={PAD.t} width={PLOT_W} height={PLOT_H} className="curve-bg" />

      {/* LED-colour strip on the Y axis: output fill maps to which LED lights. */}
      {led &&
        led.map((c, i) => {
          const segH = PLOT_H / led.length;
          return (
            <rect
              key={i}
              x={PAD.l - 8}
              y={PAD.t + PLOT_H - (i + 1) * segH}
              width={6}
              height={segH}
              fill={`rgb(${c[0]},${c[1]},${c[2]})`}
            />
          );
        })}

      {/* grid */}
      {[0.25, 0.5, 0.75].map((g) => (
        <g key={g} className="curve-grid">
          <line x1={toPx(g)} y1={PAD.t} x2={toPx(g)} y2={PAD.t + PLOT_H} />
          <line x1={PAD.l} y1={toPy(g)} x2={PAD.l + PLOT_W} y2={toPy(g)} />
        </g>
      ))}

      {/* linear reference */}
      <line className="curve-ref" x1={toPx(0)} y1={toPy(0)} x2={toPx(1)} y2={toPy(1)} />

      {/* the curve */}
      <path className="curve-line" d={path} />

      {/* control points */}
      {pts.map((p, i) => (
        <circle
          key={i}
          className={`curve-handle${drag === i ? " dragging" : ""}`}
          cx={toPx(p.x)}
          cy={toPy(p.y)}
          r={5}
        />
      ))}

      <text className="curve-axis" x={PAD.l} y={H - 6}>0</text>
      <text className="curve-axis" x={W - PAD.r} y={H - 6} textAnchor="end">RPM 100%</text>
    </svg>
  );
}

// clampPoints keeps the dragged point in [0,1] and strictly ordered between its
// neighbours (so points can't cross), matching the backend's validation.
function clampPoints(p: CurvePoint[], moved: number): CurvePoint[] {
  const next = p.map((q) => ({ ...q }));
  const lo = moved === 0 ? 0 : next[moved - 1].x + MIN_GAP;
  const hi = moved === next.length - 1 ? 1 : next[moved + 1].x - MIN_GAP;
  next[moved].x = round(Math.min(Math.max(next[moved].x, lo), Math.max(lo, hi)));
  next[moved].y = round(clamp01(next[moved].y));
  return next;
}
