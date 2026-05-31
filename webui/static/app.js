const state = {
  config: null,
};

const $ = (id) => document.getElementById(id);

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
  const t = snapshot.telemetry;
  const speed = t.Speed * 3.6;
  const ratio = t.EngineMaxRpm > 0 ? Math.min(1, Math.max(0, t.CurrentEngineRpm / t.EngineMaxRpm)) : 0;

  $("speed").textContent = speed.toFixed(0);
  $("gear").textContent = t.Gear;
  $("rpm").textContent = t.CurrentEngineRpm.toFixed(0);
  $("rpmMax").textContent = `/ ${t.EngineMaxRpm.toFixed(0)}`;
  $("rpmFill").style.width = `${ratio * 100}%`;
  $("throttle").value = t.Accel;
  $("brake").value = t.Brake;
  $("clutch").value = t.Clutch;
  setStatus(`Telemetry ${t.IsRaceOn === 1 ? "race on" : "race off"} · ${new Date(snapshot.received_at).toLocaleTimeString()}`);
}

async function refreshTelemetry() {
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

function bind() {
  $("apply").addEventListener("click", () => applyConfig().catch((error) => setStatus(error.message, "error")));
  $("save").addEventListener("click", () => saveConfig().catch((error) => setStatus(error.message, "error")));
  $("previewButtons").addEventListener("click", () => previewButtons().catch((error) => setStatus(error.message, "error")));
}

bind();
loadConfig().catch((error) => setStatus(error.message, "error"));
refreshTelemetry();
setInterval(refreshTelemetry, 200);
