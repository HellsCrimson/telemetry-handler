import { useCallback, useEffect, useRef, useState } from "react";

export type PlacementValue = {
  x: number; // overlay left within the monitor (logical px)
  y: number; // overlay top within the monitor (logical px)
  width: number;
  height: number;
  opacity: number;
  showSteering: boolean;
  steeringSize: number;
  steeringX: number; // steering offset within the overlay box
  steeringY: number;
};

type Props = {
  monitorWidth: number;
  monitorHeight: number;
  value: PlacementValue;
  onChange: (patch: Partial<PlacementValue>) => void;
};

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

// OverlayPlacement renders a scaled simulation of the monitor with a draggable
// overlay box (and steering-wheel marker inside it), so the user can position the
// overlay visually. Positions are reported back in logical monitor pixels.
export default function OverlayPlacement({ monitorWidth, monitorHeight, value, onChange }: Props) {
  const stageRef = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(1);

  // Keep the displayed scale (screen px per monitor px) in sync with the stage
  // width, so drag math and the preview stay accurate on resize.
  const measure = useCallback(() => {
    const el = stageRef.current;
    if (!el || monitorWidth <= 0) return;
    setScale(el.clientWidth / monitorWidth);
  }, [monitorWidth]);

  useEffect(() => {
    measure();
    const ro = new ResizeObserver(measure);
    if (stageRef.current) ro.observe(stageRef.current);
    return () => ro.disconnect();
  }, [measure]);

  // startDrag returns a pointerdown handler that drags `axes` and reports the new
  // value via onChange, clamped to [lo, hi] in each axis.
  const startDrag = (
    getStart: () => { x: number; y: number },
    apply: (nx: number, ny: number) => void,
    bounds: () => { minX: number; maxX: number; minY: number; maxY: number },
  ) => (e: React.PointerEvent) => {
    e.preventDefault();
    e.stopPropagation();
    const startPointerX = e.clientX;
    const startPointerY = e.clientY;
    const start = getStart();
    const s = scale || 1;
    const target = e.currentTarget;
    target.setPointerCapture(e.pointerId);

    const move = (ev: PointerEvent) => {
      const b = bounds();
      const nx = clamp(start.x + (ev.clientX - startPointerX) / s, b.minX, b.maxX);
      const ny = clamp(start.y + (ev.clientY - startPointerY) / s, b.minY, b.maxY);
      apply(Math.round(nx), Math.round(ny));
    };
    const up = (ev: PointerEvent) => {
      target.releasePointerCapture(ev.pointerId);
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  const dragBox = startDrag(
    () => ({ x: value.x, y: value.y }),
    (nx, ny) => onChange({ x: nx, y: ny }),
    () => ({
      minX: 0,
      maxX: Math.max(0, monitorWidth - value.width),
      minY: 0,
      maxY: Math.max(0, monitorHeight - value.height),
    }),
  );

  const dragSteering = startDrag(
    () => ({ x: value.steeringX, y: value.steeringY }),
    (nx, ny) => onChange({ steeringX: nx, steeringY: ny }),
    () => ({
      minX: 0,
      maxX: Math.max(0, value.width - value.steeringSize),
      minY: 0,
      maxY: Math.max(0, value.height - value.steeringSize),
    }),
  );

  const aspect = monitorWidth > 0 ? `${monitorWidth} / ${monitorHeight}` : "16 / 9";

  return (
    <div
      ref={stageRef}
      className="overlay-sim"
      style={{ aspectRatio: aspect }}
      title={`${monitorWidth}×${monitorHeight}`}
    >
      <div
        className="overlay-sim-box"
        onPointerDown={dragBox}
        style={{
          left: `${value.x * scale}px`,
          top: `${value.y * scale}px`,
          width: `${value.width * scale}px`,
          height: `${value.height * scale}px`,
          opacity: clamp(value.opacity, 0.15, 1),
        }}
      >
        <span className="overlay-sim-label">HUD</span>
        {value.showSteering && (
          <div
            className="overlay-sim-steering"
            onPointerDown={dragSteering}
            style={{
              left: `${value.steeringX * scale}px`,
              top: `${value.steeringY * scale}px`,
              width: `${value.steeringSize * scale}px`,
              height: `${value.steeringSize * scale}px`,
            }}
          />
        )}
      </div>
    </div>
  );
}
