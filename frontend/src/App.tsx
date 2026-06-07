import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Service } from "../bindings/telemetry-handler/app";
import {
  type HistorySample,
  chartDefinitions,
  readoutGroups,
  colorForField,
  formatValue,
  formatBytes,
  rgbToHex,
  hexToRgb,
} from "./telemetry";
import Chart, { type Highlight } from "./Chart";
import { TrackVisualizer } from "./TrackVisualizer";

const HISTORY_MS = 120000;

type Snapshot = { telemetry: Record<string, any>; received_at: string; available: boolean };

const TABS = [
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
  const [activeTab, setActiveTab] = useState<string>("live");
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

  const setStatus = useCallback((text: string, level: "normal" | "error" = "normal") => {
    setStatusText(text);
    setStatusLevel(level);
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
    }
    const raceText = snap.telemetry.IsRaceOn === 1 ? "race on" : "race off";
    setStatus(`Telemetry ${raceText} · ${new Date(snap.received_at).toLocaleTimeString()}`);
  }, [setStatus]);

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
    refreshRecordingStatus();
    refreshRecordingList();
    refreshOverlay();

    const telemetryTimer = window.setInterval(() => {
      if (replay.current.active) return;
      Service.GetTelemetry()
        .then((s) => mounted && ingest(s as unknown as Snapshot))
        .catch((e) => setStatus(String(e), "error"));
    }, 200);
    const statusTimer = window.setInterval(() => {
      refreshRecordingStatus();
      refreshOverlay();
    }, 1000);

    return () => {
      mounted = false;
      window.clearInterval(telemetryTimer);
      window.clearInterval(statusTimer);
    };
  }, [ingest, refreshRecordingList, setStatus]);

  // --- Config form ---
  const patch = (mutator: (c: any) => void) =>
    setConfig((c: any) => {
      const next = structuredClone(c);
      mutator(next);
      return next;
    });

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

  return (
    <>
      <header className="topbar">
        <div>
          <h1>Telemetry Handler</h1>
          <p id="status" data-level={statusLevel}>{statusText}</p>
        </div>
        <div className="actions">
          <button onClick={applyConfig}>Apply</button>
          <button onClick={saveConfig}>Save</button>
        </div>
      </header>

      <nav className="tabs" aria-label="Dashboard sections">
        {TABS.map(([id, label]) => (
          <button key={id} className={`tab${activeTab === id ? " active" : ""}`} onClick={() => setActiveTab(id)}>
            {label}
          </button>
        ))}
      </nav>

      <main>
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
              <div className="metric">
                <span>Race Position</span>
                <strong>{t?.RacePosition ?? 0}</strong>
                <small>lap {t?.LapNumber ?? 0}</small>
              </div>
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
          <ChartTab group="engineReadouts" telemetry={t} chartIds={["chartEngineMain", "chartEnginePower"]} history={history} />
        )}
        {activeTab === "inputs" && (
          <ChartTab group="inputReadouts" telemetry={t} chartIds={["chartInputsPedals", "chartInputsSteer"]} history={history} />
        )}
        {activeTab === "tires" && (
          <ChartTab group="tireReadouts" telemetry={t} chartIds={["chartTireTemp", "chartTireSlipRatio", "chartTireSlipAngle", "chartTireCombinedSlip"]} history={history} />
        )}
        {activeTab === "suspension" && (
          <ChartTab group="suspensionReadouts" telemetry={t} chartIds={["chartSuspensionNormalized", "chartSuspensionMeters"]} history={history} />
        )}
        {activeTab === "motion" && (
          <ChartTab group="motionReadouts" telemetry={t} chartIds={["chartAcceleration", "chartVelocity", "chartAngularVelocity", "chartAttitude"]} history={history} />
        )}

        {activeTab === "position" && (
          <section className="tabpage active">
            <ReadoutGroup group="positionReadouts" telemetry={t} />
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
                <label>Serial port <input autoComplete="off" placeholder="/dev/ttyACM1" value={config.moza.port} onChange={(e) => patch((c) => (c.moza.port = e.target.value))} /></label>
                <label>Update Hz <input type="number" min={1} step={1} value={config.moza.update_hz} onChange={(e) => patch((c) => (c.moza.update_hz = Number(e.target.value)))} /></label>
                <label>RPM brightness <input type="range" min={0} max={15} value={config.moza.rpm_brightness} onChange={(e) => patch((c) => (c.moza.rpm_brightness = Number(e.target.value)))} /></label>
                <label>Button mask <input type="number" min={0} max={1023} value={config.moza.button_mask} onChange={(e) => patch((c) => (c.moza.button_mask = Number(e.target.value)))} /></label>
                <button className="secondary" onClick={previewButtons}>Preview Buttons</button>
              </div>
              <div className="panel">
                <h2>MOZA Notes</h2>
                <dl className="kv">
                  <div><dt>Button mask</dt><dd>0-1023, one bit per telemetry-controlled button light</dd></div>
                  <div><dt>Preview</dt><dd>Applies colors and button mask without saving config</dd></div>
                  <div><dt>Apply</dt><dd>Updates the running MOZA driver and dashboard settings</dd></div>
                </dl>
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
              <div className="panel">
                <h2>Overlay</h2>
                <label className="check"><input type="checkbox" checked={overlayEnabled} onChange={(e) => toggleOverlay(e.target.checked)} /> Show native overlay</label>
                <dl className="kv">
                  <div><dt>Status</dt><dd>{!overlayEnabled ? "Disabled" : overlayRunning ? "Running" : "Waiting for game…"}</dd></div>
                  <div><dt>Note</dt><dd>Appears automatically while the game is sending telemetry, on the monitor the game is on (Hyprland). Set overlay.output to force a monitor.</dd></div>
                </dl>
              </div>
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

function ReadoutGroup({ group, telemetry }: { group: string; telemetry?: Record<string, any> }) {
  const rows = readoutGroups[group];
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

function ChartTab({ group, telemetry, chartIds, history }: { group: string; telemetry?: Record<string, any>; chartIds: string[]; history: HistorySample[] }) {
  return (
    <section className="tabpage active">
      <ReadoutGroup group={group} telemetry={telemetry} />
      <div className="charts">
        {chartIds.map((id) => (
          <Chart key={id} definition={chartDefinitions[id]} history={history} />
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
