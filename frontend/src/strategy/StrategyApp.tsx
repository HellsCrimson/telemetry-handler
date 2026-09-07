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
import { type SessionState, type CarState, formatLapTime } from "./model";
import { AppHeader, ContextBar, TabBar, Empty, type Fact } from "../design/Shell";
import { useSettings } from "./useSettings";
import RacePopups from "./components/RacePopups";
import TrackCircle from "./components/TrackCircle";
import WeatherPanel from "./components/WeatherPanel";
import PitParameters from "./components/PitParameters";
import PitStopEstimate from "./components/PitStopEstimate";
import PitMenuEditor from "./components/PitMenuEditor";
import WeatherForecast from "./components/WeatherForecast";
import UndercutOvercut from "./components/UndercutOvercut";
import EventTimeline from "./components/EventTimeline";
import LiveData from "./tabs/LiveData";
import DriverCoaching from "./tabs/DriverCoaching";
import CarManagement from "./tabs/CarManagement";
import DriverVs from "./tabs/DriverVs";
import SetupSheet from "./tabs/SetupSheet";
import History from "./tabs/History";
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
  ["setup", "Setup"],
  ["history", "History"],
  ["settings", "Settings"],
] as const;

const POLL_MS = 200;

// contextFacts is the one-line answer to "where are we in this race", shown in
// the bar under the header on every strategy tab.
function contextFacts(state: SessionState | null, player?: CarState): Fact[] {
  if (!state?.available) return [];
  const facts: Fact[] = [];
  if (player) {
    facts.push({ label: "LAP", value: state.max_laps > 0 ? `${player.total_laps + 1}/${state.max_laps}` : String(player.total_laps + 1) });
    facts.push({ label: "POS", value: player.place ? `P${player.place}` : "—" });
    if (player.last_lap > 0) facts.push({ label: "LAST", value: formatLapTime(player.last_lap) });
  }
  if (state.session_end_time > state.session_time) {
    facts.push({ label: "REMAIN", value: clock(state.session_end_time - state.session_time) });
  }
  facts.push({ label: "AIR", value: `${state.weather.ambient_temp.toFixed(1)}°` });
  facts.push({ label: "TRACK", value: `${state.weather.track_temp.toFixed(1)}°` });
  facts.push({ label: "RAIN", value: `${(state.weather.raining * 100).toFixed(0)}%` });
  return facts;
}

// flagBanner surfaces race control in the context bar — the one place a filled
// colour bar is warranted, because a yellow you missed is a penalty.
function flagBanner(state: SessionState | null): { kind: "fcy" | "red"; text: string } | null {
  if (!state?.available) return null;
  if (state.flags.sc_active) return { kind: "fcy", text: "SAFETY CAR DEPLOYED" };
  if (state.flags.yellow) return { kind: "fcy", text: "YELLOW FLAG" };
  return null;
}

function clock(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}` : `${m}:${String(s).padStart(2, "0")}`;
}

function sessionKind(t: number): string {
  if (t >= 10) return "RACE";
  if (t >= 5) return "QUALIFYING";
  if (t >= 1) return "PRACTICE";
  return "TEST DAY";
}

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

  const player = state ? state.cars.find((c) => c.is_player) : undefined;
  const flag = flagBanner(state);

  return (
    <div className="app-shell strategy">
      <AppHeader
        mode="strategy"
        onMode={(m) => m === "dashboard" && onExit()}
        source={available ? { label: "LMU · LIVE", rate: `${state?.cars.length ?? 0} cars` } : null}
      />

      <ContextBar
        title={state?.track || undefined}
        kind={available ? sessionKind(state?.session_type ?? 0) : undefined}
        facts={contextFacts(state, player)}
        flag={flag}
      />

      {state && <RacePopups flags={state.flags} />}

      <TabBar
        label="Strategy sections"
        active={activeTab}
        onSelect={setActiveTab}
        tabs={TABS.filter(([id]) => id !== "settings").map(([id, label]) => ({ id, label }))}
        trailing={TABS.filter(([id]) => id === "settings").map(([id, label]) => ({ id, label }))}
      />

      <main>
        {!available && activeTab !== "history" && activeTab !== "settings" && activeTab !== "setup" && (
          <div className="page">
            <Empty>
              No live session. Start Le Mans Ultimate with the lmu-bridge wrapper so the
              sidecar runs inside the Proton prefix, or replay an LMU recording.
            </Empty>
          </div>
        )}

        {available && state && activeTab === "live" && <LiveData state={state} />}
        {available && state && activeTab === "strategy" && (
          <>
            <TrackCircle state={state} pitLossSeconds={settings.pitLossSeconds} />
            <div className="strat-livedata">
              <PitParameters state={state} safetyLaps={settings.safetyLaps} />
              <PitStopEstimate strategy={state.strategy} />
              <UndercutOvercut state={state} pitLossSeconds={settings.pitLossSeconds} />
              <PitMenuEditor />
              <WeatherPanel weather={state.weather} />
              <WeatherForecast forecast={state.strategy.forecast} />
            </div>
          </>
        )}
        {available && state && activeTab === "coaching" && <DriverCoaching state={state} />}
        {available && state && activeTab === "car" && <CarManagement state={state} />}
        {available && state && activeTab === "vs" && <DriverVs state={state} />}

        {activeTab === "setup" && <SetupSheet />}
        {activeTab === "history" && <History />}
        {activeTab === "settings" && <StrategySettings settings={settings} update={updateSettings} />}

        {available && state && activeTab !== "history" && activeTab !== "setup" && <EventTimeline events={state.events} />}
      </main>
    </div>
  );
}
