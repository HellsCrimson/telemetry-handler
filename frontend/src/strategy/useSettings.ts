// Strategy settings live in the browser (localStorage), not the Go config: they
// are display/strategy preferences (pit-loss assumption, fuel safety margin) the
// strategist tweaks per session, not app configuration. Keeping them here means a
// real, working Settings tab without backend plumbing.
import { useCallback, useEffect, useState } from "react";

export type StrategySettings = {
  pitLossSeconds: number; // time a pit stop costs vs staying out — pit-window/undercut maths
  safetyLaps: number;     // fuel margin (laps) added to the "fuel to add" call
};

export const DEFAULT_SETTINGS: StrategySettings = {
  pitLossSeconds: 30,
  safetyLaps: 1,
};

const KEY = "strategy.settings";

function load(): StrategySettings {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) return { ...DEFAULT_SETTINGS, ...JSON.parse(raw) };
  } catch {
    // ignore malformed/unavailable storage — fall back to defaults
  }
  return DEFAULT_SETTINGS;
}

// useSettings returns the current settings and a patch function that persists.
export function useSettings(): [StrategySettings, (patch: Partial<StrategySettings>) => void] {
  const [settings, setSettings] = useState<StrategySettings>(load);

  useEffect(() => {
    try {
      localStorage.setItem(KEY, JSON.stringify(settings));
    } catch {
      // ignore storage write failures
    }
  }, [settings]);

  const update = useCallback((patch: Partial<StrategySettings>) => {
    setSettings((s) => ({ ...s, ...patch }));
  }, []);

  return [settings, update];
}
