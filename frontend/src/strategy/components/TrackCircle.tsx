// TrackCircle is the Strategy Planner's headline tool. It plots every car as a
// dot around a ring, positioned by how far around the lap it is and coloured by
// class, so the engineer can see the whole field at a glance and spot gaps to
// pit into. Below the ring a "pit window" readout projects where the player would
// rejoin if they pitted now (current pit-stop time cost = PIT_LOSS_SECONDS).
//
// It is deliberately SVG (not canvas): a ring of ~20 labelled dots is trivial in
// SVG, stays crisp at any size, and needs no draw loop — the component just
// re-renders when the polled SessionState changes.
import {
  type SessionState,
  type CarState,
  classColor,
  playerCar,
  PIT_LOSS_SECONDS,
} from "../model";

const SIZE = 460; // SVG viewport (square)
const R = 190; // ring radius
const C = SIZE / 2; // centre

// pointOnRing maps a 0..1 lap fraction to an x,y on the ring. 0 is the top
// (start/finish), increasing clockwise — the way a track map is usually read.
function pointOnRing(frac: number, radius = R): { x: number; y: number } {
  const angle = frac * 2 * Math.PI - Math.PI / 2;
  return { x: C + radius * Math.cos(angle), y: C + radius * Math.sin(angle) };
}

// projectedPosition re-ranks the field after adding PIT_LOSS_SECONDS to the
// player's gap-to-leader, i.e. "if the player pitted now, where would they come
// out?" Returns the projected place plus the cars immediately ahead/behind at
// the rejoin point, which is exactly the gap the engineer is hunting for.
function projectedPosition(state: SessionState, player: CarState) {
  const projectedGap = player.gap_to_leader + PIT_LOSS_SECONDS;
  // Everyone else keeps their current gap to the leader; the player drops back.
  const others = state.cars
    .filter((c) => c.id !== player.id && c.place > 0)
    .map((c) => ({ car: c, gap: c.gap_to_leader }))
    .sort((a, b) => a.gap - b.gap);
  let place = 1;
  let ahead: CarState | undefined;
  let behind: CarState | undefined;
  for (const o of others) {
    if (o.gap <= projectedGap) {
      place++;
      ahead = o.car;
    } else {
      behind = o.car;
      break;
    }
  }
  return { place, ahead, behind, projectedGap };
}

export default function TrackCircle({ state }: { state: SessionState }) {
  const player = playerCar(state);
  const cars = state.cars.filter((c) => c.place > 0 || c.lap_dist_frac > 0);

  return (
    <div className="strat-circle">
      <svg viewBox={`0 0 ${SIZE} ${SIZE}`} className="strat-circle-svg" role="img" aria-label="Track position circle">
        {/* the track ring */}
        <circle cx={C} cy={C} r={R} className="strat-ring" />
        {/* start/finish marker at the top */}
        <line x1={C} y1={C - R - 12} x2={C} y2={C - R + 12} className="strat-sf" />

        {cars.map((car) => {
          const p = pointOnRing(car.lap_dist_frac);
          const isPlayer = car.id === player?.id;
          return (
            <g key={car.id} className={`strat-dot${car.in_pits ? " in-pits" : ""}${isPlayer ? " is-player" : ""}`}>
              <circle cx={p.x} cy={p.y} r={isPlayer ? 11 : 8} fill={classColor(car.class)} stroke={isPlayer ? "#fff" : "none"} strokeWidth={isPlayer ? 2 : 0} />
              <text x={p.x} y={p.y} dy="0.35em" className="strat-dot-label">
                {car.place || "?"}
              </text>
            </g>
          );
        })}
      </svg>

      <PitWindow state={state} player={player} />
    </div>
  );
}

function PitWindow({ state, player }: { state: SessionState; player?: CarState }) {
  if (!player) return <div className="strat-pitwindow muted">No player car identified.</div>;
  const proj = projectedPosition(state, player);
  return (
    <div className="strat-pitwindow">
      <h3>Pit window estimate</h3>
      <p>
        Pit now (−{PIT_LOSS_SECONDS.toFixed(0)}s): <strong>P{player.place}</strong> → <strong>P{proj.place}</strong>
      </p>
      <p className="muted">
        Rejoin between{" "}
        {proj.ahead ? `P${proj.ahead.place} ${proj.ahead.driver || proj.ahead.car_name}` : "the front"}
        {" "}and{" "}
        {proj.behind ? `P${proj.behind.place} ${proj.behind.driver || proj.behind.car_name}` : "the back"}.
      </p>
    </div>
  );
}
