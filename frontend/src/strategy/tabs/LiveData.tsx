// LiveData is the pit wall: the classification, grouped by class, with the
// player's own car pulled out into a readout column beside it.
//
// The table is the page. Everything else on this screen exists to answer a
// question the table raises — so the classification gets the width, and the
// player panel is the sidebar rather than the other way round.
import {
  type SessionState,
  type CarState,
  type TireState,
  playerCar,
  byRaceOrder,
  formatLapTime,
  avgTemp,
  classColor,
} from "../model";
import { Panel, Stat, Empty, type StatTone } from "../../design/Shell";

const CORNERS = ["FL", "FR", "RL", "RR"];

// The column template is shared by the head and every row, so they cannot drift
// apart — the usual failure of a hand-built table.
const COLS = "30px 34px minmax(160px,1fr) 66px 62px 74px 74px 46px 34px 54px 28px 44px";

export default function LiveData({ state }: { state: SessionState }) {
  const player = playerCar(state);
  const order = byRaceOrder(state);

  if (order.length === 0) {
    return (
      <div className="page">
        <Empty>
          No cars in the session yet. The classification appears once LMU is in a
          session and the sidecar is feeding frames.
        </Empty>
      </div>
    );
  }

  // Group by class, preserving race order within each. A multi-class grid is
  // unreadable as one flat list — you are only racing your own class.
  const groups: { cls: string; cars: CarState[] }[] = [];
  for (const car of order) {
    const cls = car.class || "—";
    const last = groups[groups.length - 1];
    if (last && last.cls === cls) last.cars.push(car);
    else groups.push({ cls, cars: [car] });
  }

  const sessionBest = Math.min(...order.map((c) => c.best_lap).filter((v) => v > 0), Infinity);

  return (
    <div className="page">
      <div className="row">
        <Panel
          label="CLASSIFICATION"
          flush
          style={{ flex: 1, minWidth: 0 }}
          meta={
            <span style={{ display: "flex", gap: 12 }}>
              <Key color="var(--sem-best)">SESSION BEST</Key>
              <Key color="var(--sem-good)">PERSONAL BEST</Key>
              <Key color="var(--sem-warn)">OFF PACE</Key>
            </span>
          }
        >
          <div className="dense">
            <div className="dense-head" style={{ gridTemplateColumns: COLS }}>
              <div>POS</div>
              <div>#</div>
              <div>DRIVER</div>
              <div className="num">GAP</div>
              <div className="num">INT</div>
              <div className="num">LAST</div>
              <div className="num">BEST</div>
              <div className="mid">TYR</div>
              <div className="num">L</div>
              <div className="num">FUEL</div>
              <div className="mid">P</div>
              <div className="mid">ST</div>
            </div>

            {groups.map((group) => (
              <div key={group.cls}>
                <div className="dense-group">
                  <span className="spine" style={{ background: classColor(group.cls) }} />
                  {group.cls.toUpperCase()}
                  <span style={{ color: "var(--ink-lo)", letterSpacing: 0, fontWeight: 400 }}>
                    {group.cars.length} cars
                  </span>
                </div>
                {group.cars.map((car) => (
                  <Row key={car.id} car={car} player={player} sessionBest={sessionBest} />
                ))}
              </div>
            ))}
          </div>
        </Panel>

        {player && <PlayerColumn state={state} player={player} />}
      </div>
    </div>
  );
}

function Key({ color, children }: { color: string; children: React.ReactNode }) {
  return (
    <span style={{ display: "flex", alignItems: "center", gap: 5 }}>
      <span style={{ width: 7, height: 7, background: color }} />
      {children}
    </span>
  );
}

function Row({ car, player, sessionBest }: { car: CarState; player?: CarState; sessionBest: number }) {
  const isMe = car.id === player?.id;
  // A lap time is only meaningful against something: the session best, then the
  // car's own best. Nominal times stay ink grey.
  const lastClass =
    car.last_lap > 0 && car.last_lap <= sessionBest
      ? "v-best"
      : car.last_lap > 0 && car.best_lap > 0 && car.last_lap <= car.best_lap + 0.05
        ? "v-good"
        : car.last_lap > 0 && car.best_lap > 0 && car.last_lap > car.best_lap + 1.5
          ? "v-warn"
          : "";

  const worn = worstWear(car);
  return (
    <div
      className={`dense-row${isMe ? " is-me" : ""}`}
      style={{ gridTemplateColumns: COLS, borderLeftColor: isMe ? "var(--ink-hi)" : classColor(car.class) }}
    >
      <div className="mid" style={{ paddingLeft: 10 }}>{car.place || "—"}</div>
      <div className="num">{car.number || ""}</div>
      <div className="name">{car.driver || "—"}</div>
      <div className="num">{gap(car.gap_to_leader)}</div>
      <div className="num">{gap(car.gap_to_next)}</div>
      <div className={`num ${lastClass}`}>{formatLapTime(car.last_lap)}</div>
      <div className="num">{formatLapTime(car.best_lap)}</div>
      <div className="mid">{compoundMark(car.tires[0])}</div>
      <div className="num">{car.total_laps || 0}</div>
      <div className={`num ${fuelTone(car)}`}>{car.fuel > 0 ? car.fuel.toFixed(1) : "—"}</div>
      <div className="num">{car.num_pitstops || 0}</div>
      <div className={`mid ${car.in_pits ? "v-warn" : "v-dim"}`} style={{ fontSize: 9.5 }}>
        {car.in_pits ? "PIT" : worn > 0.75 ? "WORN" : "RUN"}
      </div>
    </div>
  );
}

/** The player's own state, beside the table rather than buried in it. */
function PlayerColumn({ state, player }: { state: SessionState; player: CarState }) {
  const stints = player.num_pitstops + 1;
  return (
    <div style={{ flex: "none", width: 320, display: "flex", flexDirection: "column", gap: "var(--s-7)" }}>
      <Panel label="YOUR CAR" meta={`STINT ${stints}`} flush>
        <div className="cell-grid" style={{ gridTemplateColumns: "1fr 1fr", border: 0, borderRadius: 0 }}>
          <Stat label="POSITION" value={player.place ? `P${player.place}` : "—"} note={`lap ${player.total_laps}`} />
          <Stat label="LAST" value={formatLapTime(player.last_lap)} />
          <Stat
            label="BEST"
            value={formatLapTime(player.best_lap)}
            tone={player.best_lap > 0 ? "best" : ""}
          />
          <Stat label="FUEL" value={player.fuel > 0 ? player.fuel.toFixed(1) : "—"} unit="L" tone={fuelStatTone(player)} />
          <Stat
            label="BATTERY"
            value={player.battery > 0 ? (player.battery * 100).toFixed(0) : "—"}
            unit="%"
          />
          <Stat label="GAP AHEAD" value={gap(player.gap_to_next)} note="interval" />
        </div>
      </Panel>

      <Panel label="TYRES" meta={player.tires[0]?.compound || "—"} flush>
        <div className="cell-grid" style={{ gridTemplateColumns: "1fr 1fr", border: 0, borderRadius: 0 }}>
          {player.tires.map((tire, i) => (
            <TyreCell key={i} corner={CORNERS[i]} tire={tire} />
          ))}
        </div>
      </Panel>

      <Panel label="CONDITIONS">
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "var(--s-3) var(--s-6)", font: "var(--t-data)" }}>
          <Fact label="AIR" value={`${state.weather.ambient_temp.toFixed(1)}°`} />
          <Fact label="TRACK" value={`${state.weather.track_temp.toFixed(1)}°`} />
          <Fact label="RAIN" value={`${(state.weather.raining * 100).toFixed(0)}%`} />
          <Fact label="WIND" value={`${state.weather.wind_max.toFixed(1)} m/s`} />
        </div>
      </Panel>
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between" }}>
      <span style={{ color: "var(--ink-lo)" }}>{label}</span>
      <span style={{ color: "var(--ink-hi)" }}>{value}</span>
    </div>
  );
}

function TyreCell({ corner, tire }: { corner: string; tire: TireState }) {
  const temp = avgTemp(tire);
  // Wear arrives as a fraction where 1 is fresh, so what is consumed is 1-w.
  const worn = 1 - tire.wear;
  return (
    <div className="stat" style={{ background: "var(--bg-inset)" }}>
      <div className="stat-label">{corner}</div>
      <div className="stat-value">
        <b style={{ fontSize: 16, color: tempColor(temp) }}>{temp > 0 ? temp.toFixed(0) : "—"}</b>
        <span className="unit">°C</span>
      </div>
      <div className="bar" style={{ marginTop: 2 }}>
        <i style={{ width: `${Math.min(100, worn * 100)}%`, background: wearColor(worn) }} />
      </div>
      <div className="stat-note">
        {(worn * 100).toFixed(0)}% worn · {tire.pressure > 0 ? tire.pressure.toFixed(0) : "—"} kPa
      </div>
    </div>
  );
}

// --- scales ---------------------------------------------------------------
// The applied scales from the design system: cold→hot for temperature,
// ok→critical for anything consumed.

function tempColor(c: number): string {
  if (c <= 0) return "var(--ink-faint)";
  if (c < 60) return "var(--sem-cold)";
  if (c < 75) return "var(--sem-cool)";
  if (c < 100) return "var(--sem-good)";
  if (c < 115) return "var(--sem-warn)";
  return "var(--sem-crit)";
}

function wearColor(worn: number): string {
  if (worn > 0.75) return "var(--sem-crit)";
  if (worn > 0.5) return "var(--sem-warn)";
  return "var(--sem-good)";
}

function worstWear(car: CarState): number {
  let worst = 0;
  for (const t of car.tires) worst = Math.max(worst, 1 - t.wear);
  return worst;
}

function fuelTone(car: CarState): string {
  if (car.fuel <= 0 || car.fuel_capacity <= 0) return "";
  const frac = car.fuel / car.fuel_capacity;
  return frac < 0.1 ? "v-crit" : frac < 0.2 ? "v-warn" : "";
}

function fuelStatTone(car: CarState): StatTone {
  if (car.fuel <= 0 || car.fuel_capacity <= 0) return "";
  const frac = car.fuel / car.fuel_capacity;
  return frac < 0.1 ? "crit" : frac < 0.2 ? "warn" : "";
}

/** gap renders an interval, or an em dash for the leader — never "+0.000". */
function gap(seconds: number): string {
  if (!seconds || seconds <= 0) return "—";
  return `+${seconds.toFixed(3)}`;
}

/** compoundMark reduces a compound name to the single letter a pit wall reads,
 *  coloured by the same scale as tyre temperature. */
function compoundMark(tire?: TireState) {
  const name = (tire?.compound || "").toUpperCase();
  if (!name) return <span className="v-absent">—</span>;
  const letter = name.includes("WET") ? "W" : name.includes("INTER") ? "I" : name[0];
  const color = name.includes("WET") || name.includes("INTER")
    ? "var(--sem-cool)"
    : name.includes("HARD")
      ? "var(--sem-cold)"
      : "var(--ink)";
  return <span style={{ color, fontSize: 10 }}>{letter}</span>;
}
