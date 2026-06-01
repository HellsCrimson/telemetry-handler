const state = {
  config: null,
  history: [],
  lastReceivedAt: "",
  charts: {},
  activeTab: "live",
  replay: {
    samples: [],
    index: 0,
    timer: null,
    playing: false,
    active: false,
    baseTime: 0,
  },
  trackVisualizer: null,
};

const HISTORY_MS = 120000;
const $ = (id) => document.getElementById(id);

const cornerFields = {
  FL: "FrontLeft",
  FR: "FrontRight",
  RL: "RearLeft",
  RR: "RearRight",
};

const semanticColors = {
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

class TrackVisualizer {
  constructor(canvasId) {
    this.canvas = $(canvasId);
    if (!this.canvas) {
      console.warn(`Canvas element with id "${canvasId}" not found`);
      return;
    }
    this.ctx = this.canvas.getContext('2d');
    this.positionData = [];
    this.currentIndex = -1;
    this.zoom = 1;
    this.panX = 0;
    this.panY = 0;
    this.isDragging = false;
    this.dragStartX = 0;
    this.dragStartY = 0;
    this.trackMinX = 0;
    this.trackMinZ = 0;
    this.trackMaxX = 100;
    this.trackMaxZ = 100;
    this.carSize = 20;
    this.padding = 40;

    this.setupEventListeners();
    this.dpiScale = window.devicePixelRatio || 1;
    this.resizeCanvas();
    window.addEventListener('resize', () => this.resizeCanvas());
  }

  resizeCanvas() {
    if (!this.canvas) return;
    const rect = this.canvas.getBoundingClientRect();
    this.canvas.width = rect.width * this.dpiScale;
    this.canvas.height = rect.height * this.dpiScale;
    this.ctx.scale(this.dpiScale, this.dpiScale);
    this.render();
  }

  setupEventListeners() {
    if (!this.canvas) return;
    this.canvas.addEventListener('wheel', (e) => this.handleZoom(e));
    this.canvas.addEventListener('mousedown', (e) => this.handleDragStart(e));
    this.canvas.addEventListener('mousemove', (e) => this.handleDrag(e));
    this.canvas.addEventListener('mouseup', () => this.handleDragEnd());
    this.canvas.addEventListener('mouseleave', () => this.handleDragEnd());
    const resetBtn = $('trackResetView');
    if (resetBtn) {
      resetBtn.addEventListener('click', () => this.resetView());
    }
  }

  handleZoom(e) {
    e.preventDefault();
    const delta = e.deltaY > 0 ? 0.9 : 1.1;
    this.zoom *= delta;
    this.zoom = Math.max(0.1, Math.min(10, this.zoom));
    this.render();
  }

  handleDragStart(e) {
    this.isDragging = true;
    this.dragStartX = e.clientX;
    this.dragStartY = e.clientY;
  }

  handleDrag(e) {
    if (!this.isDragging) return;
    const dx = e.clientX - this.dragStartX;
    const dy = e.clientY - this.dragStartY;
    this.panX += dx;
    this.panY += dy;
    this.dragStartX = e.clientX;
    this.dragStartY = e.clientY;
    this.render();
  }

  handleDragEnd() {
    this.isDragging = false;
  }

  resetView() {
    this.zoom = 1;
    this.panX = 0;
    this.panY = 0;
    this.render();
  }

  updateData(history) {
    this.positionData = history.map((sample) => ({
      x: Number(sample.telemetry.PositionX),
      z: Number(sample.telemetry.PositionZ),
      yaw: Number(sample.telemetry.Yaw),
      pitch: Number(sample.telemetry.Pitch),
      roll: Number(sample.telemetry.Roll),
      speed: Number(sample.telemetry.Speed),
    }));

    if (this.positionData.length > 0) {
      this.computeBounds();
    }
  }

  setCurrentIndex(index) {
    this.currentIndex = Math.min(index, this.positionData.length - 1);
    this.render();
  }

  computeBounds() {
    let minX = Infinity, maxX = -Infinity;
    let minZ = Infinity, maxZ = -Infinity;

    this.positionData.forEach((pos) => {
      minX = Math.min(minX, pos.x);
      maxX = Math.max(maxX, pos.x);
      minZ = Math.min(minZ, pos.z);
      maxZ = Math.max(maxZ, pos.z);
    });

    this.trackMinX = minX;
    this.trackMaxX = maxX;
    this.trackMinZ = minZ;
    this.trackMaxZ = maxZ;

    const rangeX = maxX - minX || 100;
    const rangeZ = maxZ - minZ || 100;
    const padding = Math.max(rangeX, rangeZ) * 0.1;
    this.trackMinX -= padding;
    this.trackMinZ -= padding;
    this.trackMaxX += padding;
    this.trackMaxZ += padding;
  }

  worldToCanvas(x, z) {
    const trackWidth = this.trackMaxX - this.trackMinX;
    const trackHeight = this.trackMaxZ - this.trackMinZ;
    const canvasWidth = this.canvas.width / this.dpiScale - this.padding * 2;
    const canvasHeight = this.canvas.height / this.dpiScale - this.padding * 2;

    const scale = Math.min(canvasWidth / trackWidth, canvasHeight / trackHeight) * this.zoom;
    const centerX = this.canvas.width / this.dpiScale / 2;
    const centerY = this.canvas.height / this.dpiScale / 2;

    const trackCenterX = (this.trackMinX + this.trackMaxX) / 2;
    const trackCenterZ = (this.trackMinZ + this.trackMaxZ) / 2;

    const canvasX = centerX + (x - trackCenterX) * scale + this.panX;
    const canvasY = centerY + (z - trackCenterZ) * scale + this.panY;

    return { canvasX, canvasY };
  }

  drawTrackPath() {
    if (this.positionData.length < 2) return;

    this.ctx.strokeStyle = '#42d477';
    this.ctx.lineWidth = 2;
    this.ctx.lineCap = 'round';
    this.ctx.lineJoin = 'round';
    this.ctx.beginPath();

    const start = this.worldToCanvas(this.positionData[0].x, this.positionData[0].z);
    this.ctx.moveTo(start.canvasX, start.canvasY);

    for (let i = 1; i < this.positionData.length; i++) {
      const pos = this.worldToCanvas(this.positionData[i].x, this.positionData[i].z);
      this.ctx.lineTo(pos.canvasX, pos.canvasY);
    }
    this.ctx.stroke();
  }

  drawCar(index) {
    if (index < 0 || index >= this.positionData.length) return;

    const data = this.positionData[index];
    const pos = this.worldToCanvas(data.x, data.z);

    this.ctx.save();
    this.ctx.translate(pos.canvasX, pos.canvasY);
    this.ctx.rotate((data.yaw * Math.PI) / 180);

    const width = this.carSize * 0.8;
    const height = this.carSize;

    const rollInfluence = Math.max(-1, Math.min(1, data.roll / 45));
    const pitchInfluence = Math.max(-1, Math.min(1, data.pitch / 45));

    const baseColor = [66, 212, 119];
    const hue = 120 + rollInfluence * 30;
    const saturation = 80 + pitchInfluence * 20;

    this.ctx.fillStyle = `hsl(${hue}, ${saturation}%, 45%)`;
    this.ctx.fillRect(-width / 2, -height / 2, width, height);

    this.ctx.strokeStyle = '#ffffff';
    this.ctx.lineWidth = 1;
    this.ctx.strokeRect(-width / 2, -height / 2, width, height);

    this.ctx.fillStyle = '#ffffff';
    this.ctx.fillRect(-width / 2 + 2, -height / 2 + 2, width - 4, height / 3 - 2);

    this.ctx.restore();
  }

  drawGrid() {
    const trackWidth = this.trackMaxX - this.trackMinX;
    const trackHeight = this.trackMaxZ - this.trackMinZ;
    const canvasWidth = this.canvas.width / this.dpiScale - this.padding * 2;
    const canvasHeight = this.canvas.height / this.dpiScale - this.padding * 2;

    const scale = Math.min(canvasWidth / trackWidth, canvasHeight / trackHeight) * this.zoom;

    this.ctx.strokeStyle = 'rgba(212, 218, 227, 0.1)';
    this.ctx.lineWidth = 1;
    this.ctx.font = '11px Inter, sans-serif';
    this.ctx.fillStyle = 'rgba(169, 176, 183, 0.5)';

    const gridStep = this.getGridStep(trackWidth, scale);
    const startX = Math.floor(this.trackMinX / gridStep) * gridStep;
    const startZ = Math.floor(this.trackMinZ / gridStep) * gridStep;

    for (let x = startX; x <= this.trackMaxX; x += gridStep) {
      const pos = this.worldToCanvas(x, this.trackMinZ);
      this.ctx.beginPath();
      this.ctx.moveTo(pos.canvasX, this.padding);
      this.ctx.lineTo(pos.canvasX, this.canvas.height / this.dpiScale - this.padding);
      this.ctx.stroke();
    }

    for (let z = startZ; z <= this.trackMaxZ; z += gridStep) {
      const pos = this.worldToCanvas(this.trackMinX, z);
      this.ctx.beginPath();
      this.ctx.moveTo(this.padding, pos.canvasY);
      this.ctx.lineTo(this.canvas.width / this.dpiScale - this.padding, pos.canvasY);
      this.ctx.stroke();
    }
  }

  getGridStep(trackWidth, scale) {
    const pixelStep = 50;
    const worldStep = pixelStep / scale;
    const magnitude = Math.floor(Math.log10(worldStep));
    const mantissa = Math.ceil(worldStep / Math.pow(10, magnitude));
    return mantissa * Math.pow(10, magnitude);
  }

  render() {
    if (!this.canvas || !this.ctx) return;
    const width = this.canvas.width / this.dpiScale;
    const height = this.canvas.height / this.dpiScale;

    this.ctx.fillStyle = '#080a0c';
    this.ctx.fillRect(0, 0, width, height);

    this.ctx.strokeStyle = '#343a40';
    this.ctx.lineWidth = 1;
    this.ctx.strokeRect(
      this.padding,
      this.padding,
      width - this.padding * 2,
      height - this.padding * 2
    );

    this.ctx.save();
    this.ctx.beginPath();
    this.ctx.rect(this.padding, this.padding, width - this.padding * 2, height - this.padding * 2);
    this.ctx.clip();

    this.drawGrid();
    this.drawTrackPath();
    if (this.currentIndex >= 0) {
      this.drawCar(this.currentIndex);
    }

    this.ctx.restore();

    const zoomText = `${(this.zoom * 100).toFixed(0)}%`;
    const sampleCount = this.positionData.length;
    const infoText = `Samples: ${sampleCount} | Zoom: ${zoomText}`;
    $('trackInfo').textContent = infoText;
  }
}

const readoutGroups = {
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

const chartDefinitions = {
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
  chartTireTemp: {
    title: "Tire Temperatures",
    fields: cornerSeries("TireTemp"),
  },
  chartTireSlipRatio: {
    title: "Tire Slip Ratio",
    fields: cornerSeries("TireSlipRatio"),
  },
  chartTireSlipAngle: {
    title: "Tire Slip Angle",
    fields: cornerSeries("TireSlipAngle"),
  },
  chartTireCombinedSlip: {
    title: "Tire Combined Slip",
    fields: cornerSeries("TireCombinedSlip"),
  },
  chartSuspensionNormalized: {
    title: "Normalized Suspension Travel",
    fields: cornerSeries("NormalizedSuspensionTravel"),
  },
  chartSuspensionMeters: {
    title: "Suspension Travel Meters",
    fields: cornerSeries("SuspensionTravelMeters"),
  },
  chartAcceleration: {
    title: "Acceleration",
    fields: axisSeries("Acceleration"),
  },
  chartVelocity: {
    title: "Velocity",
    fields: axisSeries("Velocity"),
  },
  chartAngularVelocity: {
    title: "Angular Velocity",
    fields: axisSeries("AngularVelocity"),
  },
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

async function api(path, options = {}) {
  const response = await fetch(path, {
    headers: { "content-type": "application/json" },
    ...options,
  });
  const body = await response.json();
  if (!response.ok) {
    throw new Error(body.error || response.statusText);
  }
  return body;
}

function cornerSeries(prefix) {
  return Object.entries(cornerFields).map(([name, suffix]) => ({
    name,
    field: `${prefix}${suffix}`,
    color: semanticColors[name],
  }));
}

function axisSeries(prefix) {
  return ["X", "Y", "Z"].map((axis) => ({ name: axis, field: `${prefix}${axis}`, color: semanticColors[axis] }));
}

function rgbToHex(rgb) {
  return `#${rgb.map((v) => Number(v).toString(16).padStart(2, "0")).join("")}`;
}

function hexToRgb(hex) {
  return [
    parseInt(hex.slice(1, 3), 16),
    parseInt(hex.slice(3, 5), 16),
    parseInt(hex.slice(5, 7), 16),
  ];
}

function setStatus(text, level = "normal") {
  const status = $("status");
  status.textContent = text;
  status.dataset.level = level;
}

function renderConfig(config) {
  state.config = config;
  $("listenAddr").value = config.listen_addr;
  $("listenPort").value = config.listen_port;
  $("printHz").value = config.print_hz;
  $("terminalPrintEnabled").checked = config.terminal_print.enabled;
  $("recordingDir").value = config.recording.dir;
  $("webEnabled").checked = config.web.enabled;
  $("webAddr").value = config.web.addr;
  $("mozaEnabled").checked = config.moza.enabled;
  $("mozaPort").value = config.moza.port;
  $("mozaUpdateHz").value = config.moza.update_hz;
  $("rpmBrightness").value = config.moza.rpm_brightness;
  $("buttonMask").value = config.moza.button_mask;
  renderColors("rpmColors", config.moza.rpm_colors);
  renderColors("buttonColors", config.moza.button_colors);
}

function renderColors(containerId, colors) {
  const container = $(containerId);
  container.textContent = "";
  colors.forEach((rgb, index) => {
    const label = document.createElement("label");
    label.className = "swatch";
    const title = document.createElement("span");
    title.textContent = String(index + 1).padStart(2, "0");
    const input = document.createElement("input");
    input.type = "color";
    input.value = rgbToHex(rgb);
    input.dataset.index = index;
    label.append(title, input);
    container.append(label);
  });
}

function readConfig() {
  const config = structuredClone(state.config);
  config.listen_addr = $("listenAddr").value.trim();
  config.listen_port = Number($("listenPort").value);
  config.print_hz = Number($("printHz").value);
  config.terminal_print.enabled = $("terminalPrintEnabled").checked;
  config.recording.dir = $("recordingDir").value.trim();
  config.web.enabled = $("webEnabled").checked;
  config.web.addr = $("webAddr").value.trim();
  config.moza.enabled = $("mozaEnabled").checked;
  config.moza.port = $("mozaPort").value.trim();
  config.moza.update_hz = Number($("mozaUpdateHz").value);
  config.moza.rpm_brightness = Number($("rpmBrightness").value);
  config.moza.button_mask = Number($("buttonMask").value);
  config.moza.rpm_colors = readColors("rpmColors");
  config.moza.button_colors = readColors("buttonColors");
  return config;
}

function readColors(containerId) {
  return [...$(containerId).querySelectorAll("input[type=color]")]
    .sort((a, b) => Number(a.dataset.index) - Number(b.dataset.index))
    .map((input) => hexToRgb(input.value));
}

function renderTelemetry(snapshot) {
  if (!snapshot.available) {
    setStatus("Waiting for telemetry");
    return;
  }

  if (snapshot.received_at !== state.lastReceivedAt) {
    state.lastReceivedAt = snapshot.received_at;
    addTelemetrySample(snapshot);
  }

  const t = snapshot.telemetry;
  const speed = t.Speed * 3.6;
  const ratio = t.EngineMaxRpm > 0 ? Math.min(1, Math.max(0, t.CurrentEngineRpm / t.EngineMaxRpm)) : 0;

  $("speed").textContent = speed.toFixed(0);
  $("gear").textContent = t.Gear;
  $("rpm").textContent = t.CurrentEngineRpm.toFixed(0);
  $("rpmMax").textContent = `/ ${t.EngineMaxRpm.toFixed(0)}`;
  $("racePosition").textContent = t.RacePosition;
  $("lapNumber").textContent = `lap ${t.LapNumber}`;
  $("rpmFill").style.width = `${ratio * 100}%`;
  $("throttle").value = t.Accel;
  $("brake").value = t.Brake;
  $("clutch").value = t.Clutch;

  renderReadouts(t);
  updateCharts();
  setStatus(`Telemetry ${t.IsRaceOn === 1 ? "race on" : "race off"} · ${new Date(snapshot.received_at).toLocaleTimeString()}`);
}

function addTelemetrySample(snapshot) {
  const time = new Date(snapshot.received_at).getTime();
  state.history.push({ time, telemetry: snapshot.telemetry });
  const cutoff = Date.now() - HISTORY_MS;
  while (state.history.length > 0 && state.history[0].time < cutoff) {
    state.history.shift();
  }
}

function renderReadouts(t) {
  Object.entries(readoutGroups).forEach(([containerId, rows]) => {
    const container = $(containerId);
    container.textContent = "";
    rows.forEach(([label, field, digits, transform, suffix]) => {
      const item = document.createElement("div");
      item.className = "readout";
      const color = colorForField({ name: label, field });
      if (color) {
        item.style.setProperty("--accent", color);
      }
      const name = document.createElement("span");
      name.textContent = label;
      const value = document.createElement("strong");
      value.textContent = formatValue(t[field], digits, transform, suffix);
      item.append(name, value);
      container.append(item);
    });
  });
}

function formatValue(value, digits, transform, suffix) {
  let next = Number(value ?? 0);
  if (transform) {
    next = transform(next);
  }
  const text = typeof digits === "number" ? next.toFixed(digits) : String(next);
  return suffix ? `${text} ${suffix}` : text;
}

function ensureCharts() {
  if (!window.echarts) {
    setStatus("ECharts failed to load", "error");
    return;
  }
  Object.keys(chartDefinitions).forEach((id) => {
    if (!state.charts[id]) {
      state.charts[id] = echarts.init($(id), "dark", { renderer: "canvas" });
    }
  });
  if (!state.trackVisualizer) {
    state.trackVisualizer = new TrackVisualizer("trackCanvas");
  }
}

function updateCharts() {
  if (!window.echarts) {
    return;
  }
  ensureCharts();
  Object.entries(chartDefinitions).forEach(([id, definition]) => {
    const chart = state.charts[id];
    if (!chart) {
      return;
    }
    const labels = state.history.map((sample) => new Date(sample.time).toLocaleTimeString());
    const series = definition.fields.map((field) => ({
      name: field.name,
      type: "line",
      showSymbol: false,
      smooth: true,
      lineStyle: { width: 2, color: colorForField(field) },
      itemStyle: { color: colorForField(field) },
      data: state.history.map((sample) => {
        const value = Number(sample.telemetry[field.field] ?? 0);
        return field.transform ? field.transform(value) : value;
      }),
    }));

    chart.setOption({
      backgroundColor: "transparent",
      title: { text: definition.title, left: 10, top: 8, textStyle: { fontSize: 13 } },
      tooltip: { trigger: "axis" },
      legend: { top: 34, type: "scroll" },
      grid: { left: 46, right: 18, top: 74, bottom: 34 },
      xAxis: { type: "category", boundaryGap: false, data: labels },
      yAxis: { type: "value", scale: true },
      series,
      animation: false,
    });
  });
  if (state.trackVisualizer) {
    state.trackVisualizer.updateData(state.history);
    state.trackVisualizer.setCurrentIndex(state.history.length - 1);
  }
}

function colorForField(field) {
  if (field.color) {
    return field.color;
  }
  if (semanticColors[field.field]) {
    return semanticColors[field.field];
  }
  if (semanticColors[field.name]) {
    return semanticColors[field.name];
  }
  if (field.field?.endsWith("FrontLeft")) {
    return semanticColors.FL;
  }
  if (field.field?.endsWith("FrontRight")) {
    return semanticColors.FR;
  }
  if (field.field?.endsWith("RearLeft")) {
    return semanticColors.RL;
  }
  if (field.field?.endsWith("RearRight")) {
    return semanticColors.RR;
  }
  if (field.field?.endsWith("X")) {
    return semanticColors.X;
  }
  if (field.field?.endsWith("Y")) {
    return semanticColors.Y;
  }
  if (field.field?.endsWith("Z")) {
    return semanticColors.Z;
  }
  return "#d8dde3";
}

async function refreshTelemetry() {
  if (state.replay.active) {
    return;
  }
  try {
    renderTelemetry(await api("/api/telemetry"));
  } catch (error) {
    setStatus(error.message, "error");
  }
}

async function loadConfig() {
  renderConfig(await api("/api/config"));
}

async function applyConfig() {
  const config = readConfig();
  renderConfig(await api("/api/config/apply", { method: "PUT", body: JSON.stringify(config) }));
  setStatus("Applied");
}

async function saveConfig() {
  const config = readConfig();
  await api("/api/config/save", { method: "PUT", body: JSON.stringify(config) });
  setStatus("Saved");
}

async function previewButtons() {
  const config = readConfig();
  await api("/api/moza/preview", { method: "POST", body: JSON.stringify(config.moza) });
  setStatus("Preview active");
}

async function refreshRecordingStatus() {
  try {
    const status = await api("/api/recording/status");
    $("recordingState").textContent = status.active ? "Recording" : "Idle";
    $("recordingFile").textContent = status.name || "-";
    $("recordingPackets").textContent = status.records || 0;
  } catch (error) {
    setStatus(error.message, "error");
  }
}

async function refreshRecordingList() {
  try {
    const recordings = await api("/api/recordings");
    const select = $("recordingSelect");
    const selected = select.value;
    select.textContent = "";
    $("recordingList").textContent = "";

    recordings.forEach((recording) => {
      const option = document.createElement("option");
      option.value = recording.name;
      option.textContent = `${recording.name} (${formatBytes(recording.size)})`;
      select.append(option);

      const item = document.createElement("button");
      item.className = "recording";
      item.type = "button";
      item.textContent = `${recording.name} · ${formatBytes(recording.size)} · ${new Date(recording.modified).toLocaleString()}`;
      item.addEventListener("click", () => {
        select.value = recording.name;
      });
      $("recordingList").append(item);
    });
    if (selected) {
      select.value = selected;
    }
  } catch (error) {
    setStatus(error.message, "error");
  }
}

async function startRecording() {
  await api("/api/recording/start", { method: "POST" });
  setStatus("Recording started");
  await refreshRecordingStatus();
}

async function stopRecording() {
  await api("/api/recording/stop", { method: "POST" });
  setStatus("Recording stopped");
  await refreshRecordingStatus();
  await refreshRecordingList();
}

async function loadReplay() {
  const name = $("recordingSelect").value;
  if (!name) {
    setStatus("No recording selected", "error");
    return;
  }
  stopReplay();
  const max = Number($("replayMax").value) || 5000;
  state.replay.samples = await api(`/api/recordings/replay?name=${encodeURIComponent(name)}&max=${max}`);
  state.replay.index = 0;
  state.replay.active = true;
  state.replay.baseTime = Date.now();
  state.history = [];
  $("replayStatus").textContent = `${state.replay.samples.length} samples loaded`;
  if (state.trackVisualizer) {
    const replayHistory = state.replay.samples.map((sample) => ({
      time: state.replay.baseTime + Number(sample.offset_ms),
      telemetry: sample.telemetry,
    }));
    state.trackVisualizer.updateData(replayHistory);
  }
  if (state.replay.samples.length > 0) {
    renderReplaySample(0);
  }
}

function playReplay() {
  if (state.replay.samples.length === 0) {
    setStatus("Load a replay first", "error");
    return;
  }
  stopReplayTimer();
  if (state.replay.index >= state.replay.samples.length) {
    state.replay.index = 0;
    state.history = [];
  }
  state.replay.active = true;
  state.replay.playing = true;
  $("replayStatus").textContent = "Replay playing";
  stepReplay();
}

function stopReplay() {
  stopReplayTimer();
  state.replay.playing = false;
  state.replay.active = false;
  state.replay.index = 0;
  $("replayStatus").textContent = state.replay.samples.length > 0 ? `${state.replay.samples.length} samples loaded` : "No replay loaded";
}

function stopReplayTimer() {
  if (state.replay.timer) {
    clearTimeout(state.replay.timer);
    state.replay.timer = null;
  }
}

function stepReplay() {
  if (!state.replay.playing || state.replay.index >= state.replay.samples.length) {
    state.replay.playing = false;
    state.replay.active = true;
    $("replayStatus").textContent = "Replay finished";
    return;
  }

  renderReplaySample(state.replay.index);
  const current = state.replay.samples[state.replay.index];
  const next = state.replay.samples[state.replay.index + 1];
  state.replay.index += 1;
  const delay = next ? Math.max(1, Math.min(250, Number(next.offset_ms - current.offset_ms))) : 1;
  state.replay.timer = setTimeout(stepReplay, delay);
}

function renderReplaySample(index) {
  const sample = state.replay.samples[index];
  const receivedAt = new Date(state.replay.baseTime + Number(sample.offset_ms)).toISOString();
  renderTelemetry({
    available: true,
    received_at: receivedAt,
    telemetry: sample.telemetry,
  });
  if (state.trackVisualizer) {
    state.trackVisualizer.setCurrentIndex(index);
  }
}

function formatBytes(bytes) {
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KB`;
  }
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function switchTab(name) {
  state.activeTab = name;
  document.querySelectorAll(".tab").forEach((tab) => tab.classList.toggle("active", tab.dataset.tab === name));
  document.querySelectorAll(".tabpage").forEach((page) => page.classList.toggle("active", page.id === `tab-${name}`));
  requestAnimationFrame(() => Object.values(state.charts).forEach((chart) => chart.resize()));
}

function bind() {
  $("apply").addEventListener("click", () => applyConfig().catch((error) => setStatus(error.message, "error")));
  $("save").addEventListener("click", () => saveConfig().catch((error) => setStatus(error.message, "error")));
  $("previewButtons").addEventListener("click", () => previewButtons().catch((error) => setStatus(error.message, "error")));
  $("recordStart").addEventListener("click", () => startRecording().catch((error) => setStatus(error.message, "error")));
  $("recordStop").addEventListener("click", () => stopRecording().catch((error) => setStatus(error.message, "error")));
  $("replayLoad").addEventListener("click", () => loadReplay().catch((error) => setStatus(error.message, "error")));
  $("replayPlay").addEventListener("click", playReplay);
  $("replayStop").addEventListener("click", stopReplay);
  document.querySelectorAll(".tab").forEach((tab) => {
    tab.addEventListener("click", () => switchTab(tab.dataset.tab));
  });
  window.addEventListener("resize", () => Object.values(state.charts).forEach((chart) => chart.resize()));
}

bind();
ensureCharts();
loadConfig().catch((error) => setStatus(error.message, "error"));
refreshRecordingStatus();
refreshRecordingList();
refreshTelemetry();
setInterval(refreshTelemetry, 200);
setInterval(refreshRecordingStatus, 1000);
