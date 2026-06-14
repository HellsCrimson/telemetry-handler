// Strategy-side model + display helpers. The data shapes themselves come from the
// generated Go bindings (engineer.SessionState / CarState / …), so this file is
// the strategy analogue of telemetry.ts: small, display-only helpers layered on
// top of the already-normalised Go model. Because the Go side does the
// game-specific mapping, nothing here is LMU-specific.
import type {
  SessionState as GoSessionState,
  CarState as GoCarState,
  TireState as GoTireState,
  FlagState as GoFlagState,
  WeatherState as GoWeatherState,
} from "../../bindings/telemetry-handler/engineer";

export type SessionState = GoSessionState;
export type CarState = GoCarState;
export type TireState = GoTireState;
export type FlagState = GoFlagState;
export type WeatherState = GoWeatherState;

// pitLossSeconds is the time a full pit stop costs relative to staying out (pit
// lane delta + service). It's a single tunable used by the pit-window estimate;
// a per-track value can replace this constant later. ~30s is typical for LMU.
export const PIT_LOSS_SECONDS = 30;

// CLASS_COLORS maps the common LMU/endurance classes to distinct colours so the
// circle reads at a glance. Unknown classes fall back to a stable hashed colour.
const CLASS_COLORS: Record<string, string> = {
  HYPERCAR: "#e23b3b",
  LMH: "#e23b3b",
  LMDH: "#e2733b",
  LMP2: "#3b82e2",
  LMGT3: "#36c46b",
  GTE: "#36c46b",
  GT3: "#36c46b",
};

// classColor resolves a class name to a colour. The lookup is case-insensitive;
// an unrecognised class gets a deterministic colour derived from its name so it
// stays consistent across frames without needing to be pre-registered.
export function classColor(cls: string): string {
  if (!cls) return "#8a93a0";
  const key = cls.trim().toUpperCase();
  if (CLASS_COLORS[key]) return CLASS_COLORS[key];
  let h = 0;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
  return `hsl(${h % 360}, 55%, 55%)`;
}

// formatGap renders a seconds gap as "+1.234" (or "+1L" style for lapped cars
// handled by the caller). 0 / leader renders as "—".
export function formatGap(seconds: number): string {
  if (!seconds || seconds <= 0) return "—";
  return `+${seconds.toFixed(3)}`;
}

// formatLapTime renders seconds as m:ss.mmm; 0 (no time set) renders as "—".
export function formatLapTime(seconds: number): string {
  if (!seconds || seconds <= 0) return "—";
  const m = Math.floor(seconds / 60);
  const s = seconds - m * 60;
  return `${m}:${s.toFixed(3).padStart(6, "0")}`;
}

// playerCar returns the car flagged as the player's (the one we're engineering
// for), falling back to a player_id match, or undefined if neither is present.
export function playerCar(state: SessionState | null): CarState | undefined {
  if (!state) return undefined;
  return (
    state.cars.find((c) => c.is_player) ??
    state.cars.find((c) => c.id === state.player_id)
  );
}

// byRaceOrder returns the cars sorted by track position (Place). Place 0 (no
// scoring) sinks to the back.
export function byRaceOrder(state: SessionState | null): CarState[] {
  if (!state) return [];
  return [...state.cars].sort((a, b) => (a.place || 999) - (b.place || 999));
}

// avgTemp averages a tire's three (left/center/right) temperature readings.
export function avgTemp(t: TireState): number {
  return (t.temp[0] + t.temp[1] + t.temp[2]) / 3;
}
