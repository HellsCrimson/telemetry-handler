// RacePopups is the global race-control banner that overlays every strategy tab.
// Phase 1 surfaces the flag / safety-car state from SessionState.Flags; later
// phases add transient popups (crashes, stationary cars, competitor pitstops)
// driven by engine-generated race events.
import { type FlagState } from "../model";

export default function RacePopups({ flags }: { flags: FlagState }) {
  const banner = bannerFor(flags);
  if (!banner) return null;
  return (
    <div className={`strat-banner strat-banner-${banner.level}`} role="status">
      {banner.text}
    </div>
  );
}

// bannerFor picks the most important active condition. Safety car outranks a
// plain yellow; green shows nothing (no banner is the calm state).
function bannerFor(flags: FlagState): { text: string; level: string } | null {
  if (flags.sc_active) return { text: "🚨 SAFETY CAR DEPLOYED", level: "sc" };
  if (flags.yellow) return { text: "⚠ YELLOW FLAG", level: "yellow" };
  if (flags.safety_car) return { text: "Safety car standing by", level: "info" };
  return null;
}
