// StrategyApp is the root of the Strategy Planner mode — the pit-wall interface a
// non-driving partner uses while their team-mate drives. It is intentionally
// self-contained: its own top bar, its own tab set, and its own polling loop, so
// the existing single-car dashboard in App.tsx is left completely untouched. The
// header toggle (onExit) flips back to the dashboard.
//
// It polls one method, Service.GetEngineerState(), which returns the whole
// game-agnostic SessionState (every car + globals) already shaped by the Go
// engineer engine. The frontend therefore stays a thin renderer.
import { useEffect, useState } from "react";
import { Service } from "../../bindings/telemetry-handler/app";
import "./strategy.css";
import { type SessionState } from "./model";
import { useSettings } from "./useSettings";
import RacePopups from "./components/RacePopups";
import TrackCircle from "./components/TrackCircle";
import WeatherPanel from "./components/WeatherPanel";
import PitParameters from "./components/PitParameters";
import UndercutOvercut from "./components/UndercutOvercut";
import EventTimeline from "./components/EventTimeline";
import LiveData from "./tabs/LiveData";
import DriverCoaching from "./tabs/DriverCoaching";
import CarManagement from "./tabs/CarManagement";
import DriverVs from "./tabs/DriverVs";
import StrategySettings from "./tabs/StrategySettings";

// The five main strategy tabs from LMU_PLAN.md plus Settings. Phase 1 implements
// Live Data and Strategy Calls (the Track Circle lives there); the rest are
// placeholders so the full layout is visible while we build it out.
const TABS = [
  ["live", "Live Data"],
  ["strategy", "Strategy Calls"],
  ["vs", "Driver Vs."],
  ["coaching", "Driver Coaching"],
  ["car", "Car Management"],
  ["settings", "Settings"],
] as const;

const POLL_MS = 200;

export default function StrategyApp({ onExit }: { onExit: () => void }) {
  const [activeTab, setActiveTab] = useState<string>("strategy");
  const [state, setState] = useState<SessionState | null>(null);
  const [settings, updateSettings] = useSettings();

  useEffect(() => {
    let mounted = true;
    const timer = setInterval(async () => {
      try {
        const s = await Service.GetEngineerState();
        if (mounted) setState(s);
      } catch {
        // transient binding error — keep the last good state
      }
    }, POLL_MS);
    return () => {
      mounted = false;
      clearInterval(timer);
    };
  }, []);

  const available = !!state?.available;

  return (
    <div className="strategy">
      <header className="topbar">
        <div>
          <h1>Strategy Planner</h1>
          <p id="status" data-level={available ? "normal" : "error"}>
            {available ? `${state?.track || "Session"} · ${state?.cars.length ?? 0} cars` : "Waiting for Le Mans Ultimate telemetry…"}
          </p>
        </div>
        <div className="actions">
          <button className="secondary" onClick={onExit}>Dashboard</button>
        </div>
      </header>

      {state && <RacePopups flags={state.flags} />}

      <nav className="tabs" aria-label="Strategy sections">
        {TABS.map(([id, label]) => (
          <button key={id} className={`tab${activeTab === id ? " active" : ""}`} onClick={() => setActiveTab(id)}>
            {label}
          </button>
        ))}
      </nav>

      <main>
        {!available && <p className="muted">No live session. Start Le Mans Ultimate (with the lmu-bridge sidecar) or replay an LMU recording.</p>}

        {available && state && activeTab === "live" && <LiveData state={state} />}
        {available && state && activeTab === "strategy" && (
          <>
            <TrackCircle state={state} pitLossSeconds={settings.pitLossSeconds} />
            <div className="strat-livedata">
              <PitParameters state={state} safetyLaps={settings.safetyLaps} />
              <UndercutOvercut state={state} pitLossSeconds={settings.pitLossSeconds} />
              <WeatherPanel weather={state.weather} />
            </div>
          </>
        )}
        {available && state && activeTab === "coaching" && <DriverCoaching state={state} />}
        {available && state && activeTab === "car" && <CarManagement state={state} />}
        {available && state && activeTab === "vs" && <DriverVs state={state} />}

        {activeTab === "settings" && <StrategySettings settings={settings} update={updateSettings} />}

        {available && state && <EventTimeline events={state.events} />}
      </main>
    </div>
  );
}
