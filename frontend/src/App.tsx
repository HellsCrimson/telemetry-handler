import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Service } from "../bindings/telemetry-handler/app";
import {
  type HistorySample,
  type Game,
  type ChartDefinition,
  chartDefinitions,
  semanticColors,
  colorForField,
  formatValue,
  formatBytes,
  rgbToHex,
  hexToRgb,
  gameFromSource,
  isFieldAvailable,
  isTabAvailable,
  filterReadoutRows,
  filterChart,
} from "./telemetry";
import Chart, { type Highlight } from "./Chart";
import { TrackVisualizer } from "./TrackVisualizer";
import OverlayPlacement, { type PlacementValue } from "./OverlayPlacement";
import { CurveEditor, presetCurve } from "./CurveEditor";
import StrategyApp from "./strategy/StrategyApp";

const HISTORY_MS = 120000;

type SnapshotMeta = { car: string; track: string; session_time: number; num_vehicles: number; steering_range_deg: number };
type Snapshot = { telemetry: Record<string, any>; received_at: string; available: boolean; source?: string; meta?: SnapshotMeta };

const TABS = [
  ["info", "Info"],
  ["live", "Live"],
  ["engine", "Engine"],
  ["inputs", "Inputs"],
  ["tires", "Tires"],
  ["suspension", "Suspension"],
  ["motion", "Motion"],
  ["position", "Position"],
  ["recording", "Recording"],
  ["review", "Review"],
  ["moza", "MOZA"],
  ["settings", "Settings"],
] as const;

export default function App() {
  // mode selects the top-level interface: the existing single-car dashboard, or
  // the multi-car Strategy Planner. It defaults to "dashboard" so nothing changes
  // for existing users; the planner is opt-in via the header toggle.
  const [mode, setMode] = useState<"dashboard" | "strategy">("dashboard");
  const [activeTab, setActiveTab] = useState<string>("info");
  const [config, setConfig] = useState<any>(null);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [history, setHistory] = useState<HistorySample[]>([]);
  const [statusText, setStatusText] = useState("Starting");
  const [statusLevel, setStatusLevel] = useState<"normal" | "error">("normal");

  const [recordingStatus, setRecordingStatus] = useState<any>({ active: false, records: 0 });
  const [recordings, setRecordings] = useState<any[]>([]);
  const [selectedRecording, setSelectedRecording] = useState("");
  const [replayMax, setReplayMax] = useState(5000);
  const [overlayEnabled, setOverlayEnabled] = useState(false);
  const [overlayRunning, setOverlayRunning] = useState(false);
  const [mozaStatus, setMozaStatus] = useState<any>({ enabled: false, connected: false, port: "", model: "", serial: "", rpm_leds: 0, wheel: "", protocol: "" });
  const [testingLights, setTestingLights] = useState(false);
  const [mozaDevices, setMozaDevices] = useState<any[]>([]);
  const [monitor, setMonitor] = useState<{ width: number; height: number; name: string; detected: boolean }>({
    width: 1920,
    height: 1080,
    name: "",
    detected: false,
  });
  const [monitorList, setMonitorList] = useState<string[]>([]);
  const [configError, setConfigError] = useState<{ path: string; error: string } | null>(null);

  // Fuel laps-remaining estimate. Neither game sends laps-of-fuel-left directly,
  // so we estimate it from consumption per completed lap (like the in-game
  // readout). perLap is in the game's fuel unit (litres for LMU, tank fraction
  // for Forza); lapsLeft is unit-agnostic.
  const [fuelEstimate, setFuelEstimate] = useState<{ perLap: number; lapsLeft: number } | null>(null);
  const fuelRef = useRef({ car: NaN, lap: -1, lapStartFuel: 0, perLapHistory: [] as number[] });

  const [report, setReport] = useState<any>(null);
  const [analyzing, setAnalyzing] = useState(false);
  const [reviewHistory, setReviewHistory] = useState<HistorySample[]>([]);
  const [minDurationMs, setMinDurationMs] = useState(150);

  // Filter out blips shorter than the user-set threshold. Point events
  // (duration 0, e.g. a short shift) are always kept — they have no span.
  const filteredEvents = useMemo<any[]>(() => {
    const all: any[] = report?.events ?? [];
    return all.filter((e) => e.duration_ms === 0 || e.duration_ms >= minDurationMs);
  }, [report, minDurationMs]);
  const hiddenCount = (report?.events?.length ?? 0) - filteredEvents.length;

  // Replay loop state kept in a ref to avoid stale closures in the setTimeout
  // chain; mirrored into React state for rendering.
  const replay = useRef({ samples: [] as any[], index: 0, playing: false, active: false, baseTime: 0, timer: 0 });
  const [replaySamples, setReplaySamples] = useState<any[]>([]);
  const [replayIndex, setReplayIndex] = useState(0);
  const [replayActive, setReplayActive] = useState(false);
  const [replayStatus, setReplayStatus] = useState("No replay loaded");

  const lastReceivedAtRef = useRef("");

  // The active game is whichever source last sent telemetry; it drives which
  // tabs and readouts are shown (LMU exposes much less data than Forza). Before
  // any telemetry arrives the game is "unknown" and the full layout is shown.
  const game: Game = gameFromSource(snapshot?.source);
  const visibleTabs = useMemo(() => TABS.filter(([id]) => isTabAvailable(game, id)), [game]);

  const setStatus = useCallback((text: string, level: "normal" | "error" = "normal") => {
    setStatusText(text);
    setStatusLevel(level);
  }, []);

  // updateFuel tracks per-lap fuel consumption and projects laps remaining.
  // It rebaselines on car change, session restart (lap going backwards) and
  // refuelling (fuel going up), so the estimate follows the in-game one.
  const updateFuel = useCallback((t: Record<string, any>) => {
    const car = Number(t.CarOrdinal ?? 0);
    const lap = Number(t.LapNumber ?? 0);
    const fuel = Number(t.Fuel ?? 0);
    const r = fuelRef.current;

    if (car !== r.car || lap < r.lap) {
      r.car = car;
      r.lap = lap;
      r.lapStartFuel = fuel;
      r.perLapHistory = [];
      setFuelEstimate(null);
      return;
    }
    if (fuel > r.lapStartFuel + 1e-4) r.lapStartFuel = fuel; // refuel / pit
    if (lap > r.lap) {
      const used = r.lapStartFuel - fuel;
      if (used > 0) {
        r.perLapHistory.push(used);
        if (r.perLapHistory.length > 5) r.perLapHistory.shift();
      }
      r.lap = lap;
      r.lapStartFuel = fuel;
    }
    if (fuel > 0 && r.perLapHistory.length > 0) {
      const avg = r.perLapHistory.reduce((a, b) => a + b, 0) / r.perLapHistory.length;
      if (avg > 0) setFuelEstimate({ perLap: avg, lapsLeft: fuel / avg });
    }
  }, []);

  const ingest = useCallback((snap: Snapshot) => {
    if (!snap.available) {
      setStatus("Waiting for telemetry");
      return;
    }
    setSnapshot(snap);
    if (snap.received_at !== lastReceivedAtRef.current) {
      lastReceivedAtRef.current = snap.received_at;
      const time = new Date(snap.received_at).getTime();
      setHistory((prev) => {
        const next = [...prev, { time, telemetry: snap.telemetry }];
        const cutoff = Date.now() - HISTORY_MS;
        while (next.length > 0 && next[0].time < cutoff) next.shift();
        return next;
      });
      updateFuel(snap.telemetry);
    }
    const raceText = snap.telemetry.IsRaceOn === 1 ? "race on" : "race off";
    setStatus(`Telemetry ${raceText} · ${new Date(snap.received_at).toLocaleTimeString()}`);
  }, [setStatus, updateFuel]);

  function refreshRecordingStatus() {
    Service.GetRecordingStatus().then(setRecordingStatus).catch(() => {});
  }

  function refreshOverlay() {
    Service.GetOverlayStatus()
      .then((s: any) => {
        setOverlayEnabled(!!s.enabled);
        setOverlayRunning(!!s.running);
      })
      .catch(() => {});
  }

  function refreshMoza() {
    Service.GetMozaStatus().then(setMozaStatus).catch(() => {});
  }

  function detectMoza() {
    Service.DetectMoza()
      .then((list: any) => setMozaDevices(Array.isArray(list) ? list : []))
      .catch(() => setMozaDevices([]));
  }

  function refreshMonitor() {
    Service.GetMonitorInfo()
      .then((m: any) => {
        if (m?.detected && m.width > 0 && m.height > 0) {
          setMonitor({ width: m.width, height: m.height, name: m.name ?? "", detected: true });
        } else {
          setMonitor((prev) => ({ ...prev, detected: false }));
        }
      })
      .catch(() => setMonitor((prev) => ({ ...prev, detected: false })));
    Service.ListMonitors()
      .then((names: any) => setMonitorList(Array.isArray(names) ? names : []))
      .catch(() => setMonitorList([]));
  }

  const refreshRecordingList = useCallback(() => {
    Service.ListRecordings()
      .then((list: any) => {
        const recs = list || [];
        setRecordings(recs);
        setSelectedRecording((prev) => prev || (recs[0]?.name ?? ""));
      })
      .catch((e) => setStatus(String(e), "error"));
  }, [setStatus]);

  // Telemetry + recording-status polling, mirroring the original 200ms / 1000ms cadence.
  useEffect(() => {
    let mounted = true;
    Service.GetConfig().then((c) => mounted && setConfig(c)).catch((e) => setStatus(String(e), "error"));
    Service.GetConfigStatus()
      .then((s: any) => mounted && setConfigError(s?.error ? { path: s.path, error: s.error } : null))
      .catch(() => {});
    refreshRecordingStatus();
    refreshRecordingList();
    refreshOverlay();
    refreshMoza();
    detectMoza();

    const telemetryTimer = window.setInterval(() => {
      if (replay.current.active) return;
      Service.GetTelemetry()
        .then((s) => mounted && ingest(s as unknown as Snapshot))
        .catch((e) => setStatus(String(e), "error"));
    }, 200);
    const statusTimer = window.setInterval(() => {
      refreshRecordingStatus();
      refreshOverlay();
      refreshMoza();
    }, 1000);

    return () => {
      mounted = false;
      window.clearInterval(telemetryTimer);
      window.clearInterval(statusTimer);
    };
  }, [ingest, refreshRecordingList, setStatus]);

  // Re-detect the target monitor whenever the Settings tab is opened, so the
  // placement preview reflects the current monitor layout.
  useEffect(() => {
    if (activeTab === "settings") refreshMonitor();
  }, [activeTab]);

  // If the detected game hides the current tab (e.g. switching to LMU while on
  // the Tires tab), fall back to the always-available Live tab.
  useEffect(() => {
    if (!isTabAvailable(game, activeTab)) setActiveTab("live");
  }, [game, activeTab]);

  // --- Config form ---
  const patch = (mutator: (c: any) => void) =>
    setConfig((c: any) => {
      const next = structuredClone(c);
      mutator(next);
      return next;
    });

  // patchPlacement maps the visual placement editor's values back onto the
  // overlay config. Free x,y placement is expressed as a top-left anchor plus
  // left/top margins (what the Wayland layer-shell uses for absolute position).
  function patchPlacement(p: Partial<PlacementValue>) {
    patch((c) => {
      const o = c.overlay;
      if (p.x !== undefined) {
        o.anchor = "top-left";
        o.margin_left = p.x;
      }
      if (p.y !== undefined) {
        o.anchor = "top-left";
        o.margin_top = p.y;
      }
      if (p.steeringX !== undefined) o.steering_x = p.steeringX;
      if (p.steeringY !== undefined) o.steering_y = p.steeringY;
    });
  }

  async function applyConfig() {
    try {
      const updated = await Service.ApplyConfig(config);
      setConfig(updated);
      setStatus("Applied");
    } catch (e) {
      setStatus(String(e), "error");
    }
  }

  async function saveConfig() {
    try {
      await Service.SaveConfig(config);
      setStatus("Saved");
    } catch (e) {
      setStatus(String(e), "error");
    }
  }

  async function previewButtons() {
    try {
      await Service.PreviewMoza(config.moza);
      setStatus("Preview active");
    } catch (e) {
      setStatus(String(e), "error");
    }
  }

  async function testLights() {
    setTestingLights(true);
    setStatus("Testing rev lights…");
    try {
      await Service.TestMozaLights();
      setStatus("Light test complete");
    } catch (e) {
      setStatus(String(e), "error");
    } finally {
      setTestingLights(false);
    }
  }

  async function toggleOverlay(enabled: boolean) {
    setOverlayEnabled(enabled);
    try {
      await Service.SetOverlayEnabled(enabled);
      setStatus(enabled ? "Overlay enabled — shows when the game is running" : "Overlay disabled");
      refreshOverlay();
    } catch (e) {
      setStatus(String(e), "error");
    }
  }

  // --- Recording ---
  async function startRecording() {
    try {
      await Service.StartRecording();
      setStatus("Recording started");
      refreshRecordingStatus();
    } catch (e) {
      setStatus(String(e), "error");
    }
  }

  async function stopRecording() {
    try {
      await Service.StopRecording();
      setStatus("Recording stopped");
      refreshRecordingStatus();
      refreshRecordingList();
    } catch (e) {
      setStatus(String(e), "error");
    }
  }

  // --- Review / coaching analysis ---
  async function analyzeRecording() {
    if (!selectedRecording) {
      setStatus("No recording selected", "error");
      return;
    }
    setAnalyzing(true);
    setReport(null);
    setReviewHistory([]);
    try {
      const [r, samples] = await Promise.all([
        Service.AnalyzeRecording(selectedRecording, 0),
        Service.ReplayRecording(selectedRecording, 0) as Promise<any[]>,
      ]);
      setReport(r);
      setReviewHistory(buildReviewHistory(samples || []));
      setStatus(`Analyzed ${selectedRecording} · ${(r as any).events?.length ?? 0} findings`);
    } catch (e) {
      setStatus(String(e), "error");
    } finally {
      setAnalyzing(false);
    }
  }

  // --- Replay ---
  function stopReplayTimer() {
    if (replay.current.timer) {
      window.clearTimeout(replay.current.timer);
      replay.current.timer = 0;
    }
  }

  function renderReplaySample(index: number) {
    const sample = replay.current.samples[index];
    if (!sample) return;
    const receivedAt = new Date(replay.current.baseTime + Number(sample.offset_ms)).toISOString();
    ingest({ available: true, received_at: receivedAt, telemetry: sample.telemetry });
    setReplayIndex(index);
  }

  function stepReplay() {
    const r = replay.current;
    if (!r.playing || r.index >= r.samples.length) {
      r.playing = false;
      r.active = true;
      setReplayStatus("Replay finished");
      return;
    }
    renderReplaySample(r.index);
    const current = r.samples[r.index];
    const next = r.samples[r.index + 1];
    r.index += 1;
    const delay = next ? Math.max(1, Math.min(250, Number(next.offset_ms) - Number(current.offset_ms))) : 1;
    r.timer = window.setTimeout(stepReplay, delay);
  }

  function stopReplay() {
    stopReplayTimer();
    replay.current.playing = false;
    replay.current.active = false;
    replay.current.index = 0;
    setReplayActive(false);
    setReplayStatus(replay.current.samples.length > 0 ? `${replay.current.samples.length} samples loaded` : "No replay loaded");
  }

  async function loadReplay() {
    if (!selectedRecording) {
      setStatus("No recording selected", "error");
      return;
    }
    stopReplay();
    try {
      const samples = (await Service.ReplayRecording(selectedRecording, replayMax || 5000)) as any[];
      replay.current.samples = samples || [];
      replay.current.index = 0;
      replay.current.active = true;
      replay.current.baseTime = Date.now();
      setReplaySamples(replay.current.samples);
      setReplayActive(true);
      setHistory([]);
      lastReceivedAtRef.current = "";
      setReplayStatus(`${replay.current.samples.length} samples loaded`);
      if (replay.current.samples.length > 0) renderReplaySample(0);
    } catch (e) {
      setStatus(String(e), "error");
    }
  }

  function playReplay() {
    if (replay.current.samples.length === 0) {
      setStatus("Load a replay first", "error");
      return;
    }
    stopReplayTimer();
    if (replay.current.index >= replay.current.samples.length) {
      replay.current.index = 0;
      setHistory([]);
    }
    replay.current.active = true;
    replay.current.playing = true;
    setReplayActive(true);
    setReplayStatus("Replay playing");
    stepReplay();
  }

  // Track path + current index: full replay path during replay, rolling history otherwise.
  const trackHistory = useMemo<HistorySample[]>(() => {
    if (replayActive) {
      return replaySamples.map((s) => ({ time: replay.current.baseTime + Number(s.offset_ms), telemetry: s.telemetry }));
    }
    return history;
  }, [replayActive, replaySamples, history]);
  const trackIndex = replayActive ? replayIndex : history.length - 1;

  const t = snapshot?.telemetry;

  // Strategy Planner is a distinct, self-contained interface (its own polling and
  // tabs). Render it instead of the dashboard when selected; all dashboard hooks
  // above have already run, so this conditional return is safe.
  if (mode === "strategy") {
    return <StrategyApp onExit={() => setMode("dashboard")} />;
  }

  return (
    <>
      <header className="topbar">
        <div>
          <h1>Telemetry Handler</h1>
          <p id="status" data-level={statusLevel}>{statusText}</p>
        </div>
        <div className="actions">
          <button className="secondary" onClick={() => setMode("strategy")}>Strategy Planner</button>
          <button onClick={applyConfig}>Apply</button>
          <button onClick={saveConfig}>Save</button>
        </div>
      </header>

      {configError && (
        <div className="banner banner-error" role="alert">
          <strong>Configuration error.</strong> Could not load <code>{configError.path}</code>: {configError.error}. Running with default settings — fix the file and restart, or adjust settings below and Save to overwrite it.
          <button className="banner-dismiss" onClick={() => setConfigError(null)} aria-label="Dismiss">×</button>
        </div>
      )}

      <nav className="tabs" aria-label="Dashboard sections">
        {visibleTabs.map(([id, label]) => (
          <button key={id} className={`tab${activeTab === id ? " active" : ""}`} onClick={() => setActiveTab(id)}>
            {label}
          </button>
        ))}
      </nav>

      <main>
        {activeTab === "info" && (
          <InfoPage game={game} telemetry={t} meta={snapshot?.meta} source={snapshot?.source} receivedAt={snapshot?.received_at} />
        )}

        {activeTab === "live" && (
          <section className="tabpage active">
            <div className="telemetry">
              <div className="metric speed">
                <span>Speed</span>
                <strong>{((t?.Speed ?? 0) * 3.6).toFixed(0)}</strong>
                <small>km/h</small>
              </div>
              <div className="metric">
                <span>Gear</span>
                <strong>{t?.Gear ?? 0}</strong>
              </div>
              <div className="metric">
                <span>RPM</span>
                <strong>{(t?.CurrentEngineRpm ?? 0).toFixed(0)}</strong>
                <small>/ {(t?.EngineMaxRpm ?? 0).toFixed(0)}</small>
              </div>
              {isFieldAvailable(game, "RacePosition") ? (
                <div className="metric">
                  <span>Race Position</span>
                  <strong>{t?.RacePosition ?? 0}</strong>
                  <small>lap {t?.LapNumber ?? 0}</small>
                </div>
              ) : (
                <div className="metric">
                  <span>Lap</span>
                  <strong>{t?.LapNumber ?? 0}</strong>
                </div>
              )}
              <div className="barblock">
                <div className="rpmbar">
                  <div id="rpmFill" style={{ width: `${rpmRatio(t) * 100}%` }} />
                </div>
                <div className="pedals">
                  <label>Throttle <meter min={0} max={255} value={t?.Accel ?? 0} /></label>
                  <label>Brake <meter min={0} max={255} value={t?.Brake ?? 0} /></label>
                  <label>Clutch <meter min={0} max={255} value={t?.Clutch ?? 0} /></label>
                </div>
              </div>
            </div>
            <div className="charts one">
              <Chart definition={chartDefinitions.chartLive} history={history} />
            </div>
          </section>
        )}

        {activeTab === "engine" && (
          <ChartTab group="engineReadouts" telemetry={t} chartIds={["chartEngineMain", "chartEnginePower"]} history={history} game={game}
            extra={<FuelEstimate game={game} estimate={fuelEstimate} />} />
        )}
        {activeTab === "inputs" && (
          <ChartTab group="inputReadouts" telemetry={t} chartIds={["chartInputsPedals", "chartInputsSteer"]} history={history} game={game} />
        )}
        {activeTab === "tires" && (
          <ChartTab group="tireReadouts" telemetry={t} chartIds={["chartTireTemp", "chartTireSlipRatio", "chartTireSlipAngle", "chartTireCombinedSlip"]} history={history} game={game} />
        )}
        {activeTab === "suspension" && (
          <ChartTab group="suspensionReadouts" telemetry={t} chartIds={["chartSuspensionNormalized", "chartSuspensionMeters"]} history={history} game={game} />
        )}
        {activeTab === "motion" && (
          <ChartTab group="motionReadouts" telemetry={t} chartIds={["chartAcceleration", "chartVelocity", "chartAngularVelocity", "chartAttitude"]} history={history} game={game} />
        )}

        {activeTab === "position" && (
          <section className="tabpage active">
            <ReadoutGroup group="positionReadouts" telemetry={t} game={game} />
            <TrackPanel history={trackHistory} currentIndex={trackIndex} onClear={() => setHistory([])} />
            <div className="charts">
              <Chart definition={chartDefinitions.chartLapTiming} history={history} />
            </div>
          </section>
        )}

        {activeTab === "recording" && (
          <section className="tabpage active">
            <div className="settings">
              <div className="panel">
                <h2>Capture</h2>
                <dl className="kv">
                  <div><dt>Status</dt><dd>{recordingStatus.active ? "Recording" : "Idle"}</dd></div>
                  <div><dt>File</dt><dd>{recordingStatus.name || "-"}</dd></div>
                  <div><dt>Packets</dt><dd>{recordingStatus.records || 0}</dd></div>
                </dl>
                <div className="rowactions">
                  <button onClick={startRecording}>Start Recording</button>
                  <button className="secondary" onClick={stopRecording}>Stop</button>
                </div>
              </div>
              <div className="panel">
                <h2>Replay</h2>
                <label>Recording
                  <select value={selectedRecording} onChange={(e) => setSelectedRecording(e.target.value)}>
                    {recordings.map((r) => (
                      <option key={r.name} value={r.name}>{r.name} ({formatBytes(r.size)})</option>
                    ))}
                  </select>
                </label>
                <label>Max samples
                  <input type="number" min={1} step={100} value={replayMax} onChange={(e) => setReplayMax(Number(e.target.value))} />
                </label>
                <div className="rowactions">
                  <button onClick={loadReplay}>Load</button>
                  <button className="secondary" onClick={playReplay}>Play</button>
                  <button className="secondary" onClick={stopReplay}>Stop</button>
                </div>
                <p className="hint">{replayStatus}</p>
              </div>
            </div>
            <div className="panel">
              <h2>Saved Files</h2>
              <div className="recordings">
                {recordings.map((r) => (
                  <button key={r.name} className="recording" type="button" onClick={() => setSelectedRecording(r.name)}>
                    {r.name} · {formatBytes(r.size)} · {new Date(r.modified).toLocaleString()}
                  </button>
                ))}
              </div>
            </div>
          </section>
        )}

        {activeTab === "review" && (
          <section className="tabpage active">
            <div className="panel">
              <h2>Coaching Analysis</h2>
              <p className="hint">Pick a recorded session and analyze it for braking, throttle and grip mistakes.</p>
              <label>Recording
                <select value={selectedRecording} onChange={(e) => setSelectedRecording(e.target.value)}>
                  {recordings.map((r) => (
                    <option key={r.name} value={r.name}>{r.name} ({formatBytes(r.size)})</option>
                  ))}
                </select>
              </label>
              <div className="rowactions">
                <button onClick={analyzeRecording} disabled={analyzing}>{analyzing ? "Analyzing…" : "Analyze"}</button>
              </div>
              <label>Min issue duration: {(minDurationMs / 1000).toFixed(2)} s
                <input type="range" min={0} max={1500} step={50} value={minDurationMs}
                  onChange={(e) => setMinDurationMs(Number(e.target.value))} />
              </label>
              {report && hiddenCount > 0 && (
                <p className="hint">{hiddenCount} brief event{hiddenCount === 1 ? "" : "s"} hidden below {(minDurationMs / 1000).toFixed(2)} s.</p>
              )}
              {report?.notes?.length > 0 && report.notes.map((n: string, i: number) => (
                <p className="hint" key={i}>{n}</p>
              ))}
            </div>
            {report && <Scorecard report={report} />}
            {report && reviewHistory.length > 0 && (
              <ReviewCharts history={reviewHistory} events={filteredEvents} />
            )}
            {report && <EventList events={filteredEvents} />}
          </section>
        )}

        {activeTab === "moza" && config && (
          <section className="tabpage active">
            <div className="settings">
              <div className="panel">
                <h2>MOZA Output</h2>
                <label className="check"><input type="checkbox" checked={config.moza.enabled} onChange={(e) => patch((c) => (c.moza.enabled = e.target.checked))} /> Enabled</label>
                <label>Serial port <input autoComplete="off" placeholder="auto-detect" value={config.moza.port} onChange={(e) => patch((c) => (c.moza.port = e.target.value))} /></label>
                <p className="hint">Leave blank to use the detected wheel automatically.</p>
                <label>Update Hz <input type="number" min={1} step={1} value={config.moza.update_hz} onChange={(e) => patch((c) => (c.moza.update_hz = Number(e.target.value)))} /></label>
                <label>RPM brightness <input type="range" min={0} max={15} value={config.moza.rpm_brightness} onChange={(e) => patch((c) => (c.moza.rpm_brightness = Number(e.target.value)))} /></label>
                <label>RPM LEDs (rim) <input type="number" min={0} max={16} value={config.moza.rpm_leds ?? 0} onChange={(e) => patch((c) => (c.moza.rpm_leds = Number(e.target.value)))} /></label>
                <p className="hint">Rev-light count on the rim. 0 = auto ({mozaStatus.rpm_leds || "default"}). Set this to match your rim if the lights look wrong — the rim model can't be detected over USB.</p>
                <div className="curve-field">
                  <span className="field-label">RPM curve</span>
                  <CurveEditor
                    points={config.moza.rpm_curve_points ?? []}
                    colors={config.moza.rpm_colors}
                    onChange={(pts) => patch((c) => (c.moza.rpm_curve_points = pts))}
                  />
                  <div className="curve-presets">
                    {[["linear", "Linear"], ["exponential", "Exponential"], ["logarithmic", "Logarithmic"], ["scurve", "S-curve"]].map(([id, label]) => (
                      <button key={id} type="button" className="secondary" onClick={() => patch((c) => (c.moza.rpm_curve_points = presetCurve(id)))}>{label}</button>
                    ))}
                  </div>
                  <p className="hint">How RPM maps onto the rev-light bar (left = idle, right = max RPM; the colour strip shows where each LED sits). <strong>Drag</strong> a point to bend the curve, <strong>click</strong> empty space to add one, <strong>double-click</strong> to remove. Bow the curve below the diagonal to keep the green LEDs lit across a wider RPM range and squeeze red + redline into a small window near the top — handy when the engine rarely reaches 100% RPM. Presets just seed the points.</p>
                </div>
                <label>Button mask <input type="number" min={0} max={1023} value={config.moza.button_mask} onChange={(e) => patch((c) => (c.moza.button_mask = Number(e.target.value)))} /></label>
                <div className="actions">
                  <button className="secondary" onClick={previewButtons}>Preview Buttons</button>
                  <button className="secondary" onClick={testLights} disabled={!mozaStatus.connected || testingLights} title={mozaStatus.connected ? "Sweep the rev lights to confirm they work" : "Connect a wheel first"}>
                    {testingLights ? "Testing…" : "Test Lights"}
                  </button>
                </div>
              </div>
              <div className="panel">
                <h2>Connected Wheel</h2>
                <dl className="kv">
                  <div><dt>Status</dt><dd>{!mozaStatus.enabled ? "Disabled" : mozaStatus.connected ? "Connected" : "Waiting for wheel…"}</dd></div>
                  <div><dt>Wheel</dt><dd>{mozaStatus.connected ? (mozaStatus.wheel ? `${mozaStatus.wheel}${mozaStatus.protocol ? ` (${mozaStatus.protocol} protocol)` : ""}` : "Unknown rim") : "—"}</dd></div>
                  <div><dt>Base</dt><dd>{mozaStatus.connected ? (mozaStatus.model || "Unrecognised MOZA") : "—"}</dd></div>
                  <div><dt>Serial</dt><dd>{mozaStatus.connected && mozaStatus.serial ? mozaStatus.serial : "—"}</dd></div>
                  <div><dt>RPM LEDs</dt><dd>{mozaStatus.connected ? mozaStatus.rpm_leds : "—"}</dd></div>
                </dl>
                <h3 className="subhead">Detected over USB</h3>
                <p className="hint">MOZA wheels found on the system. Use one to fill the serial port automatically.</p>
                {mozaDevices.length === 0 ? (
                  <p className="hint">No MOZA wheel detected.</p>
                ) : (
                  <ul className="device-list">
                    {mozaDevices.map((d: any) => (
                      <li key={d.port}>
                        <button
                          className={config.moza.port === d.port ? "" : "secondary"}
                          onClick={() => patch((c) => (c.moza.port = d.port))}
                        >
                          {d.model} <span className="muted">· {d.port}</span>
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
                <button className="secondary" onClick={detectMoza}>Rescan</button>
              </div>
            </div>
            <section className="colorgrid">
              <div>
                <h2>RPM Colors</h2>
                <ColorSwatches colors={config.moza.rpm_colors} onChange={(i, rgb) => patch((c) => (c.moza.rpm_colors[i] = rgb))} />
              </div>
              <div>
                <h2>Button Colors</h2>
                <ColorSwatches colors={config.moza.button_colors} onChange={(i, rgb) => patch((c) => (c.moza.button_colors[i] = rgb))} />
              </div>
            </section>
          </section>
        )}

        {activeTab === "settings" && config && (
          <section className="tabpage active">
            <div className="settings">
              <div className="panel">
                <h2>Receiver</h2>
                <label>Listen address <input autoComplete="off" value={config.listen_addr} onChange={(e) => patch((c) => (c.listen_addr = e.target.value))} /></label>
                <label>Listen port <input type="number" min={1} max={65535} value={config.listen_port} onChange={(e) => patch((c) => (c.listen_port = Number(e.target.value)))} /></label>
                <label>Print Hz <input type="number" min={0.1} step={0.1} value={config.print_hz} onChange={(e) => patch((c) => (c.print_hz = Number(e.target.value)))} /></label>
                <label className="check"><input type="checkbox" checked={config.terminal_print.enabled} onChange={(e) => patch((c) => (c.terminal_print.enabled = e.target.checked))} /> Terminal telemetry output</label>
                <label>Recording directory <input autoComplete="off" value={config.recording.dir} onChange={(e) => patch((c) => (c.recording.dir = e.target.value))} /></label>
              </div>
              {(() => {
                const o = config.overlay;
                const num = (v: any, d: number) => (typeof v === "number" ? v : d);
                const width = num(o.width, 320);
                const height = num(o.height, 210);
                const steeringSize = num(o.steering_size, 60);
                const placement: PlacementValue = {
                  x: num(o.margin_left, 0),
                  y: num(o.margin_top, 0),
                  width,
                  height,
                  opacity: num(o.opacity, 0.85),
                  showSteering: !!o.show_steering,
                  steeringSize,
                  steeringX: num(o.steering_x, width - steeringSize - 64),
                  steeringY: num(o.steering_y, 8),
                };
                return (
                  <div className="panel" style={{ gridColumn: "1 / -1" }}>
                    <h2>Overlay</h2>
                    <label className="check"><input type="checkbox" checked={overlayEnabled} onChange={(e) => toggleOverlay(e.target.checked)} /> Show native overlay</label>
                    <dl className="kv">
                      <div><dt>Status</dt><dd>{!overlayEnabled ? "Disabled" : overlayRunning ? "Running" : "Waiting for game…"}</dd></div>
                      <div><dt>Monitor</dt><dd>{monitor.detected ? `${monitor.name ?? ""} ${monitor.width}×${monitor.height}`.trim() : `${monitor.width}×${monitor.height} (not auto-detected — set manually below)`}</dd></div>
                    </dl>

                    <h3 className="subhead">Placement</h3>
                    <p className="hint">Drag the box to position the overlay; drag the wheel marker to move the steering display. Saved as an x,y pixel position.</p>
                    <OverlayPlacement monitorWidth={monitor.width} monitorHeight={monitor.height} value={placement} onChange={patchPlacement} />
                    <div className="grid2">
                      <label>X (px) <input type="number" min={0} value={placement.x} onChange={(e) => patchPlacement({ x: Number(e.target.value) })} /></label>
                      <label>Y (px) <input type="number" min={0} value={placement.y} onChange={(e) => patchPlacement({ y: Number(e.target.value) })} /></label>
                    </div>
                    {!monitor.detected && (
                      <div className="grid2">
                        <label>Screen width <input type="number" min={1} value={monitor.width} onChange={(e) => setMonitor((m) => ({ ...m, width: Number(e.target.value) }))} /></label>
                        <label>Screen height <input type="number" min={1} value={monitor.height} onChange={(e) => setMonitor((m) => ({ ...m, height: Number(e.target.value) }))} /></label>
                      </div>
                    )}

                    <h3 className="subhead">Monitor & detection</h3>
                    {(() => {
                      const current = o.output && o.output !== "auto" ? o.output : "";
                      const opts = current && !monitorList.includes(current) ? [...monitorList, current] : monitorList;
                      return (
                        <label>Monitor
                          <select value={current} onChange={(e) => patch((c) => (c.overlay.output = e.target.value))}>
                            <option value="">Auto (follow the game's monitor)</option>
                            {opts.map((name) => (<option key={name} value={name}>{name}</option>))}
                          </select>
                        </label>
                      );
                    })()}
                    <label>Game window match <input autoComplete="off" placeholder="forza" value={o.game_window_match ?? ""} onChange={(e) => patch((c) => (c.overlay.game_window_match = e.target.value))} /></label>
                    <p className="hint">Auto-detection (Hyprland) finds the monitor whose window class/title contains this text. Pick a monitor above to force one.</p>

                    <h3 className="subhead">Dimensions & appearance</h3>
                    <div className="grid2">
                      <label>Width <input type="number" min={1} value={width} onChange={(e) => patch((c) => (c.overlay.width = Number(e.target.value)))} /></label>
                      <label>Height <input type="number" min={1} value={height} onChange={(e) => patch((c) => (c.overlay.height = Number(e.target.value)))} /></label>
                    </div>
                    <label>Opacity ({placement.opacity.toFixed(2)}) <input type="range" min={0.1} max={1} step={0.05} value={placement.opacity} onChange={(e) => patch((c) => (c.overlay.opacity = Number(e.target.value)))} /></label>
                    <label>Update Hz <input type="number" min={1} max={60} step={1} value={num(o.update_hz, 10)} onChange={(e) => patch((c) => (c.overlay.update_hz = Number(e.target.value)))} /></label>

                    <h3 className="subhead">Steering wheel</h3>
                    <label className="check"><input type="checkbox" checked={!!o.show_steering} onChange={(e) => patch((c) => (c.overlay.show_steering = e.target.checked))} /> Show steering wheel</label>
                    <div className="grid2">
                      <label>Size <input type="number" min={16} max={256} value={steeringSize} onChange={(e) => patch((c) => (c.overlay.steering_size = Number(e.target.value)))} /></label>
                      <label>Wheel X (px) <input type="number" min={0} value={placement.steeringX} onChange={(e) => patchPlacement({ steeringX: Number(e.target.value) })} /></label>
                      <label>Wheel Y (px) <input type="number" min={0} value={placement.steeringY} onChange={(e) => patchPlacement({ steeringY: Number(e.target.value) })} /></label>
                      <label>Rotation range (°) <input type="number" min={90} max={1800} step={10} value={num(o.steering_range_deg, 1080)} onChange={(e) => patch((c) => (c.overlay.steering_range_deg = Number(e.target.value)))} /></label>
                    </div>
                    <p className="hint">Lock-to-lock wheel rotation. Used for Forza; LMU reports its own per-car range, which takes over automatically.</p>
                    <label>Custom wheel image <input autoComplete="off" placeholder="/path/to/wheel.png (optional, square PNG)" value={o.steering_image_path ?? ""} onChange={(e) => patch((c) => (c.overlay.steering_image_path = e.target.value))} /></label>
                    <p className="hint">Apply restarts a running overlay so changes take effect immediately. Save to persist to config.json.</p>
                  </div>
                );
              })()}
            </div>
          </section>
        )}
      </main>
    </>
  );
}

function rpmRatio(t?: Record<string, any>): number {
  if (!t || !(t.EngineMaxRpm > 0)) return 0;
  return Math.min(1, Math.max(0, t.CurrentEngineRpm / t.EngineMaxRpm));
}

const GAME_NAMES: Record<string, string> = {
  forza: "Forza Motorsport",
  lmu: "Le Mans Ultimate",
};

const DRIVETRAINS = ["FWD", "RWD", "AWD"];

// formatSessionTime renders elapsed seconds as h:mm:ss (or m:ss under an hour).
function formatSessionTime(seconds: number): string {
  if (!(seconds > 0)) return "—";
  const total = Math.floor(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const ss = String(s).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

// InfoPage is the landing overview: which game is feeding telemetry, the session
// status, and descriptive car/track info. Rows are game-aware — LMU shows the
// car/track names and session time from the metadata channel; Forza shows the
// car identifiers carried in its telemetry struct. Only populated rows render.
function InfoPage({ game, telemetry, meta, source, receivedAt }: {
  game: Game;
  telemetry?: Record<string, any>;
  meta?: SnapshotMeta;
  source?: string;
  receivedAt?: string;
}) {
  const t = telemetry;

  const session: [string, string][] = [
    ["Game", source ? GAME_NAMES[source] ?? source : "—"],
    ["Status", receivedAt ? (t?.IsRaceOn === 1 ? "Race on" : "Race off") : "Waiting for telemetry"],
  ];
  if (receivedAt) session.push(["Last update", new Date(receivedAt).toLocaleTimeString()]);
  if (game === "lmu" && meta) {
    if (meta.session_time > 0) session.push(["Session time", formatSessionTime(meta.session_time)]);
    if (meta.num_vehicles > 0) session.push(["Cars in session", String(meta.num_vehicles)]);
  }

  const car: [string, string][] = [];
  if (game === "lmu") {
    if (meta?.car) car.push(["Car", meta.car]);
    if (meta?.track) car.push(["Track", meta.track]);
    if (meta && meta.steering_range_deg > 0) car.push(["Steering range", `${Math.round(meta.steering_range_deg)}°`]);
  } else if (t) {
    if (t.CarOrdinal) car.push(["Car ordinal", `#${t.CarOrdinal}`]);
    if (t.DrivetrainType >= 0 && t.DrivetrainType <= 2) car.push(["Drivetrain", DRIVETRAINS[t.DrivetrainType]]);
    if (t.NumCylinders) car.push(["Cylinders", String(t.NumCylinders)]);
    if (t.CarClass) car.push(["Car class", String(t.CarClass)]);
    if (t.CarPerformanceIndex) car.push(["Performance index", String(t.CarPerformanceIndex)]);
    if (t.CarGroup) car.push(["Car group", String(t.CarGroup)]);
  }

  return (
    <section className="tabpage active">
      <div className="settings">
        <div className="panel">
          <h2>Session</h2>
          <dl className="kv">
            {session.map(([k, v]) => (
              <div key={k}><dt>{k}</dt><dd>{v}</dd></div>
            ))}
          </dl>
        </div>
        {car.length > 0 && (
          <div className="panel">
            <h2>Car &amp; Track</h2>
            <dl className="kv">
              {car.map(([k, v]) => (
                <div key={k}><dt>{k}</dt><dd>{v}</dd></div>
              ))}
            </dl>
          </div>
        )}
      </div>
    </section>
  );
}

// FuelEstimate shows the projected laps of fuel remaining (estimated from
// per-lap consumption) plus the per-lap burn. perLap is litres for LMU and a
// tank fraction for Forza, so it is formatted per game.
function FuelEstimate({ game, estimate }: { game: Game; estimate: { perLap: number; lapsLeft: number } | null }) {
  const lapsLeft = estimate ? estimate.lapsLeft.toFixed(1) : "—";
  const perLap = !estimate
    ? "—"
    : game === "lmu"
      ? `${estimate.perLap.toFixed(2)} L`
      : `${(estimate.perLap * 100).toFixed(1)} %`;
  const accent = { ["--accent" as any]: semanticColors.Fuel };
  return (
    <div className="readouts">
      <div className="readout" style={accent}>
        <span>Fuel laps left (est.)</span>
        <strong>{lapsLeft}</strong>
      </div>
      <div className="readout" style={accent}>
        <span>Fuel / lap</span>
        <strong>{perLap}</strong>
      </div>
    </div>
  );
}

function ReadoutGroup({ group, telemetry, game = "unknown" }: { group: string; telemetry?: Record<string, any>; game?: Game }) {
  const rows = filterReadoutRows(group, game);
  return (
    <div className="readouts">
      {rows.map(([label, field, digits, transform, suffix], i) => {
        const color = colorForField({ name: label, field });
        return (
          <div className="readout" key={i} style={{ ["--accent" as any]: color }}>
            <span>{label}</span>
            <strong>{formatValue(telemetry?.[field], digits, transform, suffix)}</strong>
          </div>
        );
      })}
    </div>
  );
}

function ChartTab({ group, telemetry, chartIds, history, game, extra }: { group: string; telemetry?: Record<string, any>; chartIds: string[]; history: HistorySample[]; game: Game; extra?: ReactNode }) {
  // Drop series the active game does not provide, and skip charts left empty.
  const charts = chartIds
    .map((id) => ({ id, definition: filterChart(id, game) }))
    .filter((c): c is { id: string; definition: ChartDefinition } => c.definition !== null);
  return (
    <section className="tabpage active">
      <ReadoutGroup group={group} telemetry={telemetry} game={game} />
      {extra}
      <div className="charts">
        {charts.map(({ id, definition }) => (
          <Chart key={id} definition={definition} history={history} />
        ))}
      </div>
    </section>
  );
}

const EVENT_LABELS: Record<string, string> = {
  brake_lockup: "Brake lockup",
  corner_wheelspin: "Corner wheelspin",
  under_driving: "Throttle left unused",
  coasting: "Coasting",
  pedal_overlap: "Throttle/brake overlap",
  over_rev: "Bouncing off limiter",
  short_shift: "Short shift",
};

const SEVERITY_COLORS: Record<string, string> = {
  major: "#e85d5d",
  minor: "#e6c84f",
  info: "#5b9bd5",
};

function formatDuration(ms: number): string {
  if (!ms) return "instant";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}

function scoreColor(score: number): string {
  if (score >= 75) return "#42d477";
  if (score >= 50) return "#e6c84f";
  return "#e85d5d";
}

function Scorecard({ report }: { report: any }) {
  const laps: any[] = report.laps ?? [];
  const overall = report.overall;
  const rows = [...laps, overall].filter(Boolean);
  return (
    <div className="panel">
      <h2>Scorecard</h2>
      <div className="scorecard">
        <table>
          <thead>
            <tr>
              <th>Lap</th><th>Lap time</th><th>Score</th>
              <th>Wheelspin</th><th>Lockup</th><th>Coasting</th><th>Throttle</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i} className={r.lap === -1 ? "overall" : ""}>
                <td>{r.lap === -1 ? "Overall" : r.lap}</td>
                <td>{r.lap_time > 0 ? `${r.lap_time.toFixed(2)}s` : "—"}</td>
                <td><strong style={{ color: scoreColor(r.overall_score) }}>{r.overall_score.toFixed(0)}</strong></td>
                <td>{r.wheelspin_pct.toFixed(0)}%</td>
                <td>{r.lockup_pct.toFixed(0)}%</td>
                <td>{r.coasting_pct.toFixed(0)}%</td>
                <td>{r.throttle_score.toFixed(0)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function EventList({ events }: { events: any[] }) {
  return (
    <div className="panel">
      <h2>Findings ({events.length})</h2>
      {events.length === 0 && <p className="hint">No issues detected — clean driving.</p>}
      <div className="findings">
        {events.map((e, i) => (
          <div className="finding" key={i} style={{ ["--accent" as any]: SEVERITY_COLORS[e.severity] ?? "#888" }}>
            <div className="finding-head">
              <span className="finding-kind">{EVENT_LABELS[e.kind] ?? e.kind}</span>
              <span className="finding-meta">Lap {e.lap} · {(Number(e.offset_ms) / 1000).toFixed(1)}s · lasted {formatDuration(e.duration_ms)} · {e.speed.toFixed(0)} km/h</span>
            </div>
            <div className="finding-msg">{e.message}</div>
            <div className="finding-tip">{e.suggestion}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

// Keep the analysis charts responsive on long recordings by downsampling to at
// most this many points (markArea zones still map by sample index).
const REVIEW_MAX_POINTS = 4000;

function buildReviewHistory(samples: any[]): HistorySample[] {
  const n = samples.length;
  if (n === 0) return [];
  const step = Math.max(1, Math.ceil(n / REVIEW_MAX_POINTS));
  const out: HistorySample[] = [];
  for (let i = 0; i < n; i += step) {
    out.push({ time: Number(samples[i].offset_ms), telemetry: samples[i].telemetry });
  }
  return out;
}

// timeToIndex finds the first history sample at or after the given offset (ms).
// history is sorted by time, so a binary search suffices.
function timeToIndex(history: HistorySample[], ms: number): number {
  let lo = 0;
  let hi = history.length - 1;
  let ans = history.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (history[mid].time >= ms) {
      ans = mid;
      hi = mid - 1;
    } else {
      lo = mid + 1;
    }
  }
  return ans;
}

const SEVERITY_AREA: Record<string, string> = {
  major: "rgba(232,93,93,0.22)",
  minor: "rgba(230,200,79,0.20)",
  info: "rgba(91,155,213,0.16)",
};

// Each review chart shows a slice of the telemetry the detectors rely on, and
// shades only the event kinds that pertain to it.
const REVIEW_CHARTS: { id: string; kinds: string[] }[] = [
  { id: "chartInputsPedals", kinds: ["coasting", "pedal_overlap", "under_driving", "brake_lockup"] },
  { id: "chartTireSlipRatio", kinds: ["brake_lockup", "corner_wheelspin"] },
  { id: "chartTireCombinedSlip", kinds: ["under_driving", "corner_wheelspin", "brake_lockup"] },
  { id: "chartEngineMain", kinds: ["over_rev", "short_shift"] },
  { id: "chartInputsSteer", kinds: ["corner_wheelspin"] },
];

function formatClock(ms: number): string {
  const total = ms / 1000;
  const m = Math.floor(total / 60);
  const s = total - m * 60;
  return `${m}:${s.toFixed(1).padStart(4, "0")}`;
}

function buildHighlights(history: HistorySample[], events: any[], kinds: string[]): Highlight[] {
  if (history.length === 0) return [];
  return events
    .filter((e) => kinds.includes(e.kind))
    .map((e) => {
      const start = Number(e.offset_ms);
      const end = start + Number(e.duration_ms || 0);
      const from = timeToIndex(history, start);
      let to = timeToIndex(history, end);
      if (to <= from) to = Math.min(history.length - 1, from + 1); // keep zero-length events visible
      return {
        fromIndex: from,
        toIndex: to,
        color: SEVERITY_AREA[e.severity] ?? "rgba(150,150,150,0.16)",
        name: EVENT_LABELS[e.kind] ?? e.kind,
      };
    });
}

function ReviewCharts({ history, events }: { history: HistorySample[]; events: any[] }) {
  const labels = useMemo(() => history.map((s) => formatClock(s.time)), [history]);
  return (
    <div className="panel">
      <h2>Telemetry with highlighted issues</h2>
      <p className="hint">Shaded zones mark where the analysis found something — colour shows severity (red major, yellow minor, blue info).</p>
      <div className="charts">
        {REVIEW_CHARTS.map((rc) => (
          <Chart
            key={rc.id}
            definition={chartDefinitions[rc.id]}
            history={history}
            xLabels={labels}
            highlights={buildHighlights(history, events, rc.kinds)}
          />
        ))}
      </div>
    </div>
  );
}

function ColorSwatches({ colors, onChange }: { colors: number[][]; onChange: (index: number, rgb: number[]) => void }) {
  return (
    <div className="swatches">
      {colors.map((rgb, index) => (
        <label className="swatch" key={index}>
          <span>{String(index + 1).padStart(2, "0")}</span>
          <input type="color" value={rgbToHex(rgb)} onChange={(e) => onChange(index, hexToRgb(e.target.value))} />
        </label>
      ))}
    </div>
  );
}

function TrackPanel({ history, currentIndex, onClear }: { history: HistorySample[]; currentIndex: number; onClear: () => void }) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const vizRef = useRef<TrackVisualizer | null>(null);
  const [info, setInfo] = useState("Zoom: scroll | Pan: drag");

  useEffect(() => {
    if (!canvasRef.current) return;
    const viz = new TrackVisualizer(canvasRef.current, setInfo);
    vizRef.current = viz;
    return () => {
      viz.destroy();
      vizRef.current = null;
    };
  }, []);

  useEffect(() => {
    const viz = vizRef.current;
    if (!viz) return;
    viz.updateData(history);
    viz.setCurrentIndex(currentIndex);
  }, [history, currentIndex]);

  return (
    <div className="track-container">
      <div className="track-controls">
        <button onClick={() => vizRef.current?.resetView()}>Reset View</button>
        <button onClick={onClear}>Clear</button>
        <span>{info}</span>
      </div>
      <canvas ref={canvasRef} id="trackCanvas" />
    </div>
  );
}
