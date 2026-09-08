// Interface magnification.
//
// The layout is drawn at a deliberate density — 9.5px micro labels, 28px table
// rows, fixed 48/32/36px chrome — which reads correctly at 1080p and small on a
// 1440p or 4K panel. Rather than letting individual type sizes drift apart per
// display, the whole webview is magnified: proportions hold, and the text is
// re-rasterised rather than scaled, so it stays crisp.
//
// Applied natively (webkit_web_view_set_zoom_level via Wails) and persisted in
// config.json, because a display preference that resets every launch is worse
// than none.

import { useCallback, useEffect, useState } from "react";
import { Service } from "../../bindings/telemetry-handler/app";

/** The steps the shortcuts and the picker move through. Coarse on purpose: this
 *  is a setting you land on once, not something to fine-tune.
 *
 *  Nothing below 1.0 is offered because Wails clamps it away on Linux
 *  (linuxWebviewWindow.setZoom raises anything under 1 back to 1), so a 90%
 *  option would sit in the menu doing nothing. The design is already drawn at
 *  its intended density at 100%. */
export const UI_SCALES: readonly number[] = [1.0, 1.1, 1.25, 1.4, 1.6, 1.8, 2.0];

const DEFAULT_SCALE = 1.0;

export function useUIScale() {
  const [scale, setScale] = useState<number>(DEFAULT_SCALE);

  useEffect(() => {
    Service.GetUIScale()
      .then((v) => {
        if (v > 0) setScale(v);
      })
      .catch(() => {
        /* the binding is unavailable outside the app; the default stands */
      });
  }, []);

  // The Go side clamps and returns what it actually applied, so the UI shows the
  // real value rather than what was asked for.
  const apply = useCallback((next: number) => {
    setScale(next);
    Service.SetUIScale(next)
      .then((applied) => {
        if (applied > 0) setScale(applied);
      })
      .catch(() => {
        /* leave the optimistic value; the log carries the error */
      });
  }, []);

  const step = useCallback(
    (direction: 1 | -1) => {
      setScale((current) => {
        // Snap to the nearest step first, so a value typed into config.json
        // still steps sensibly.
        let index = 0;
        let best = Infinity;
        UI_SCALES.forEach((v, i) => {
          const d = Math.abs(v - current);
          if (d < best) {
            best = d;
            index = i;
          }
        });
        const next = UI_SCALES[Math.min(UI_SCALES.length - 1, Math.max(0, index + direction))];
        if (next !== current) apply(next);
        return current;
      });
    },
    [apply],
  );

  const reset = useCallback(() => apply(DEFAULT_SCALE), [apply]);

  // Ctrl +/-/0, as every desktop app does it. Registered once for the whole
  // interface rather than per screen.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      if (e.key === "+" || e.key === "=") {
        e.preventDefault();
        step(1);
      } else if (e.key === "-" || e.key === "_") {
        e.preventDefault();
        step(-1);
      } else if (e.key === "0") {
        e.preventDefault();
        reset();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [step, reset]);

  return { scale, setScale: apply, step, reset };
}
