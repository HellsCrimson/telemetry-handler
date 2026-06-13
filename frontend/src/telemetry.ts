// Telemetry field definitions, semantic colours, chart and readout layouts.
// Ported from the original webui/static/app.js so the dashboard renders the
// same panels and series.

export type Telemetry = Record<string, number>;

export interface HistorySample {
  time: number;
  telemetry: Telemetry;
}

export interface ChartField {
  name: string;
  field: string;
  color?: string;
  transform?: (v: number) => number;
}

export interface ChartDefinition {
  title: string;
  fields: ChartField[];
}

// [label, field, digits?, transform?, suffix?]
export type ReadoutRow = [string, string, number?, (((v: number) => number) | null)?, string?];

const cornerFields: Record<string, string> = {
  FL: "FrontLeft",
  FR: "FrontRight",
  RL: "RearLeft",
  RR: "RearRight",
};

export const semanticColors: Record<string, string> = {
  Accel: "#42d477",
  Throttle: "#42d477",
  Brake: "#e85d5d",
  Clutch: "#4aa3ff",
  HandBrake: "#f2a541",
  Steer: "#d8dde3",
  NormalizedDrivingLine: "#9bdb6d",
  NormalizedAIBrakeDifference: "#ff7aa2",
  Speed: "#42d477",
  CurrentEngineRpm: "#e6c84f",
  EngineMaxRpm: "#9aa3ad",
  Power: "#b88cff",
  Torque: "#ffb86b",
  Boost: "#4aa3ff",
  Fuel: "#7bd88f",
  FL: "#42d477",
  FR: "#e6c84f",
  RL: "#4aa3ff",
  RR: "#e85d5d",
  X: "#e85d5d",
  Y: "#42d477",
  Z: "#4aa3ff",
  Yaw: "#e6c84f",
  Pitch: "#4aa3ff",
  Roll: "#e85d5d",
  BestLap: "#42d477",
  LastLap: "#4aa3ff",
  CurrentLap: "#e6c84f",
  CurrentRaceTime: "#d8dde3",
};

function cornerSeries(prefix: string): ChartField[] {
  return Object.entries(cornerFields).map(([name, suffix]) => ({
    name,
    field: `${prefix}${suffix}`,
    color: semanticColors[name],
  }));
}

function axisSeries(prefix: string): ChartField[] {
  return ["X", "Y", "Z"].map((axis) => ({
    name: axis,
    field: `${prefix}${axis}`,
    color: semanticColors[axis],
  }));
}

export const readoutGroups: Record<string, ReadoutRow[]> = {
  engineReadouts: [
    ["Engine Max RPM", "EngineMaxRpm", 0],
    ["Engine Idle RPM", "EngineIdleRpm", 0],
    ["Current RPM", "CurrentEngineRpm", 0],
    ["Speed", "Speed", 1, (v) => v * 3.6, "km/h"],
    ["Power", "Power", 0, (v) => v / 1000, "kW"],
    ["Torque", "Torque", 0, null, "Nm"],
    ["Boost", "Boost", 1, null, "psi"],
    ["Fuel", "Fuel", 1, null, "%"],
    ["Gear", "Gear"],
    ["Drivetrain", "DrivetrainType"],
    ["Cylinders", "NumCylinders"],
  ],
  inputReadouts: [
    ["Throttle", "Accel"],
    ["Brake", "Brake"],
    ["Clutch", "Clutch"],
    ["Handbrake", "HandBrake"],
    ["Steer", "Steer"],
    ["Driving Line", "NormalizedDrivingLine"],
    ["AI Brake Diff", "NormalizedAIBrakeDifference"],
  ],
  tireReadouts: [
    ["Temp FL", "TireTempFrontLeft", 1],
    ["Temp FR", "TireTempFrontRight", 1],
    ["Temp RL", "TireTempRearLeft", 1],
    ["Temp RR", "TireTempRearRight", 1],
    ["Slip Ratio FL", "TireSlipRatioFrontLeft", 3],
    ["Slip Angle FL", "TireSlipAngleFrontLeft", 3],
    ["Combined Slip FL", "TireCombinedSlipFrontLeft", 3],
    ["Wheel Speed FL", "WheelRotationSpeedFrontLeft", 2],
  ],
  suspensionReadouts: [
    ["Norm Travel FL", "NormalizedSuspensionTravelFrontLeft", 3],
    ["Norm Travel FR", "NormalizedSuspensionTravelFrontRight", 3],
    ["Norm Travel RL", "NormalizedSuspensionTravelRearLeft", 3],
    ["Norm Travel RR", "NormalizedSuspensionTravelRearRight", 3],
    ["Travel FL", "SuspensionTravelMetersFrontLeft", 3, null, "m"],
    ["Travel FR", "SuspensionTravelMetersFrontRight", 3, null, "m"],
    ["Travel RL", "SuspensionTravelMetersRearLeft", 3, null, "m"],
    ["Travel RR", "SuspensionTravelMetersRearRight", 3, null, "m"],
  ],
  motionReadouts: [
    ["Accel X", "AccelerationX", 2],
    ["Accel Y", "AccelerationY", 2],
    ["Accel Z", "AccelerationZ", 2],
    ["Velocity X", "VelocityX", 2],
    ["Velocity Y", "VelocityY", 2],
    ["Velocity Z", "VelocityZ", 2],
    ["Angular X", "AngularVelocityX", 2],
    ["Angular Y", "AngularVelocityY", 2],
    ["Angular Z", "AngularVelocityZ", 2],
    ["Yaw", "Yaw", 2],
    ["Pitch", "Pitch", 2],
    ["Roll", "Roll", 2],
  ],
  positionReadouts: [
    ["Position X", "PositionX", 1],
    ["Position Y", "PositionY", 1],
    ["Position Z", "PositionZ", 1],
    ["Distance", "DistanceTraveled", 1, null, "m"],
    ["Lap", "LapNumber"],
    ["Race Position", "RacePosition"],
    ["Best Lap", "BestLap", 3, null, "s"],
    ["Last Lap", "LastLap", 3, null, "s"],
    ["Current Lap", "CurrentLap", 3, null, "s"],
    ["Race Time", "CurrentRaceTime", 3, null, "s"],
  ],
};

export const chartDefinitions: Record<string, ChartDefinition> = {
  chartLive: {
    title: "Live Overview",
    fields: [
      { name: "Speed km/h", field: "Speed", color: semanticColors.Speed, transform: (v) => v * 3.6 },
      { name: "RPM x100", field: "CurrentEngineRpm", color: semanticColors.CurrentEngineRpm, transform: (v) => v / 100 },
      { name: "Throttle", field: "Accel" },
      { name: "Brake", field: "Brake" },
    ],
  },
  chartEngineMain: {
    title: "RPM and Speed",
    fields: [
      { name: "RPM", field: "CurrentEngineRpm" },
      { name: "Max RPM", field: "EngineMaxRpm" },
      { name: "Speed km/h", field: "Speed", color: semanticColors.Speed, transform: (v) => v * 3.6 },
    ],
  },
  chartEnginePower: {
    title: "Powertrain",
    fields: [
      { name: "Power kW", field: "Power", color: semanticColors.Power, transform: (v) => v / 1000 },
      { name: "Torque Nm", field: "Torque" },
      { name: "Boost", field: "Boost" },
      { name: "Fuel", field: "Fuel" },
    ],
  },
  chartInputsPedals: {
    title: "Pedals",
    fields: [
      { name: "Throttle", field: "Accel" },
      { name: "Brake", field: "Brake" },
      { name: "Clutch", field: "Clutch" },
      { name: "Handbrake", field: "HandBrake" },
    ],
  },
  chartInputsSteer: {
    title: "Steering and Assists",
    fields: [
      { name: "Steer", field: "Steer" },
      { name: "Driving Line", field: "NormalizedDrivingLine" },
      { name: "AI Brake Diff", field: "NormalizedAIBrakeDifference" },
    ],
  },
  chartTireTemp: { title: "Tire Temperatures", fields: cornerSeries("TireTemp") },
  chartTireSlipRatio: { title: "Tire Slip Ratio", fields: cornerSeries("TireSlipRatio") },
  chartTireSlipAngle: { title: "Tire Slip Angle", fields: cornerSeries("TireSlipAngle") },
  chartTireCombinedSlip: { title: "Tire Combined Slip", fields: cornerSeries("TireCombinedSlip") },
  chartSuspensionNormalized: { title: "Normalized Suspension Travel", fields: cornerSeries("NormalizedSuspensionTravel") },
  chartSuspensionMeters: { title: "Suspension Travel Meters", fields: cornerSeries("SuspensionTravelMeters") },
  chartAcceleration: { title: "Acceleration", fields: axisSeries("Acceleration") },
  chartVelocity: { title: "Velocity", fields: axisSeries("Velocity") },
  chartAngularVelocity: { title: "Angular Velocity", fields: axisSeries("AngularVelocity") },
  chartAttitude: {
    title: "Yaw / Pitch / Roll",
    fields: [
      { name: "Yaw", field: "Yaw" },
      { name: "Pitch", field: "Pitch" },
      { name: "Roll", field: "Roll" },
    ],
  },
  chartLapTiming: {
    title: "Lap Timing",
    fields: [
      { name: "Best", field: "BestLap", color: semanticColors.BestLap },
      { name: "Last", field: "LastLap", color: semanticColors.LastLap },
      { name: "Current", field: "CurrentLap", color: semanticColors.CurrentLap },
      { name: "Race Time", field: "CurrentRaceTime", color: semanticColors.CurrentRaceTime },
    ],
  },
};

// --- Per-game capabilities ---
// The receiver demultiplexes Forza and LMU; each TelemetrySnapshot reports the
// game that produced it. LMU exposes far less data than Forza (it is mapped into
// the same forza.Telemetry model, leaving most fields empty), so the dashboard
// hides tabs and readouts that game cannot fill.

export type Game = "forza" | "lmu" | "unknown";

// Telemetry fields actually populated by LMU (see app/lmu_adapter.go). Anything
// outside this set is empty/meaningless under LMU and is hidden.
const LMU_FIELDS = new Set<string>([
  "IsRaceOn",
  "CurrentEngineRpm",
  "EngineMaxRpm",
  "Speed",
  "Gear",
  "Accel",
  "Brake",
  "Clutch",
  "Steer",
  "Fuel",
  "LapNumber",
  "RacePosition",
]);

// Tabs hidden per game: LMU has no tire/suspension/motion/position telemetry,
// and recordings are Forza-only (so Recording/Review do not apply).
const HIDDEN_TABS: Partial<Record<Game, string[]>> = {
  lmu: ["tires", "suspension", "motion", "position", "recording", "review"],
};

export function gameFromSource(source?: string): Game {
  return source === "forza" || source === "lmu" ? source : "unknown";
}

// isFieldAvailable reports whether a telemetry field carries real data for the
// given game. Forza (and the unknown/pre-telemetry state) render the full superset.
export function isFieldAvailable(game: Game, field: string): boolean {
  if (game === "lmu") return LMU_FIELDS.has(field);
  return true;
}

export function isTabAvailable(game: Game, tab: string): boolean {
  return !(HIDDEN_TABS[game] ?? []).includes(tab);
}

export function filterReadoutRows(group: string, game: Game): ReadoutRow[] {
  const rows = readoutGroups[group] ?? [];
  return rows.filter(([, field]) => isFieldAvailable(game, field));
}

// filterChart returns the chart definition with unavailable series removed, or
// null when nothing remains so the caller can skip rendering the panel.
export function filterChart(id: string, game: Game): ChartDefinition | null {
  const def = chartDefinitions[id];
  if (!def) return null;
  const fields = def.fields.filter((f) => isFieldAvailable(game, f.field));
  if (fields.length === 0) return null;
  return { ...def, fields };
}

export function colorForField(field: ChartField): string {
  if (field.color) return field.color;
  if (semanticColors[field.field]) return semanticColors[field.field];
  if (semanticColors[field.name]) return semanticColors[field.name];
  if (field.field?.endsWith("FrontLeft")) return semanticColors.FL;
  if (field.field?.endsWith("FrontRight")) return semanticColors.FR;
  if (field.field?.endsWith("RearLeft")) return semanticColors.RL;
  if (field.field?.endsWith("RearRight")) return semanticColors.RR;
  if (field.field?.endsWith("X")) return semanticColors.X;
  if (field.field?.endsWith("Y")) return semanticColors.Y;
  if (field.field?.endsWith("Z")) return semanticColors.Z;
  return "#d8dde3";
}

export function formatValue(
  value: number | undefined,
  digits?: number,
  transform?: ((v: number) => number) | null,
  suffix?: string,
): string {
  let next = Number(value ?? 0);
  if (transform) next = transform(next);
  const text = typeof digits === "number" ? next.toFixed(digits) : String(next);
  return suffix ? `${text} ${suffix}` : text;
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export function rgbToHex(rgb: number[]): string {
  return `#${rgb.map((v) => Number(v).toString(16).padStart(2, "0")).join("")}`;
}

export function hexToRgb(hex: string): number[] {
  return [
    parseInt(hex.slice(1, 3), 16),
    parseInt(hex.slice(3, 5), 16),
    parseInt(hex.slice(5, 7), 16),
  ];
}
