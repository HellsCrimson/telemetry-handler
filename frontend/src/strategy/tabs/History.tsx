// History reads the local SQLite store (via bindings) and shows past sessions and
// the indexed recordings — the longer-term record the DB enables. It's read-only
// and refreshes on open + on demand.
import { useCallback, useEffect, useState } from "react";
import { Service } from "../../../bindings/telemetry-handler/app";
import type { SessionRow, RecordingRow } from "../../../bindings/telemetry-handler/store";
import { formatLapTime } from "../model";

function fmtDate(unixSeconds: number): string {
  if (!unixSeconds) return "—";
  return new Date(unixSeconds * 1000).toLocaleString();
}

export default function History() {
  const [sessions, setSessions] = useState<SessionRow[]>([]);
  const [recordings, setRecordings] = useState<RecordingRow[]>([]);

  const refresh = useCallback(async () => {
    try {
      const [s, r] = await Promise.all([Service.ListSessions(), Service.ListIndexedRecordings()]);
      setSessions(s ?? []);
      setRecordings(r ?? []);
    } catch {
      // store unavailable — leave lists empty
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <div className="strat-history">
      <div className="strat-history-head">
        <h3>History</h3>
        <button className="secondary" onClick={() => void refresh()}>Refresh</button>
      </div>

      <section className="strat-group">
        <h3>Sessions</h3>
        {sessions.length === 0 ? (
          <p className="muted">No sessions recorded yet. (They’re logged automatically once a session starts.)</p>
        ) : (
          <table className="strat-table">
            <thead>
              <tr><th>Started</th><th>Track</th><th>Car</th><th>Laps</th><th>Best</th></tr>
            </thead>
            <tbody>
              {sessions.map((s) => (
                <tr key={s.id}>
                  <td>{fmtDate(s.started_at)}</td>
                  <td>{s.track || "—"}</td>
                  <td>{s.car || "—"}</td>
                  <td>{s.laps}</td>
                  <td>{formatLapTime(s.best_lap)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="strat-group">
        <h3>Recordings</h3>
        {recordings.length === 0 ? (
          <p className="muted">No indexed recordings yet. (Recordings are indexed when you stop recording.)</p>
        ) : (
          <table className="strat-table">
            <thead>
              <tr><th>Recorded</th><th>Track</th><th>Car</th><th>Source</th><th>Size</th></tr>
            </thead>
            <tbody>
              {recordings.map((r) => (
                <tr key={r.name}>
                  <td>{fmtDate(r.recorded_at)}</td>
                  <td>{r.track || "—"}</td>
                  <td>{r.car || "—"}</td>
                  <td>{r.source || "—"}</td>
                  <td>{(r.size_bytes / 1024 / 1024).toFixed(1)} MB</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
