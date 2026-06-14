// UndercutOvercut is an advisory pit-strategy tool. It frames the call around the
// one number that decides it: the pit-loss. To pass the car ahead by pitting
// (undercut), your fresh-tyre pace has to recover (pit-loss − gap) seconds before
// they cover the same stop; to keep the car behind from undercutting YOU, they
// face the same sum against their gap. It states the seconds-to-find rather than a
// false verdict, because the actual tyre-delta isn't in the telemetry.
import { type SessionState, playerCar, byRaceOrder } from "../model";

export default function UndercutOvercut({ state, pitLossSeconds }: { state: SessionState; pitLossSeconds: number }) {
  const player = playerCar(state);
  if (!player || !player.place) return null;

  const order = byRaceOrder(state);
  const idx = order.findIndex((c) => c.id === player.id);
  const ahead = idx > 0 ? order[idx - 1] : undefined;
  const behind = idx >= 0 && idx < order.length - 1 ? order[idx + 1] : undefined;

  // gap to the car ahead is player.gap_to_next; gap of the car behind to us is its
  // own gap_to_next.
  const gapAhead = player.gap_to_next;
  const gapBehind = behind?.gap_to_next ?? 0;

  return (
    <section className="strat-group strat-undercut">
      <h3>Undercut / Overcut</h3>
      <p className="muted strat-axis-note">Assuming a pit costs {pitLossSeconds.toFixed(0)} s. Lower “to find” = easier.</p>
      <div className="strat-readouts">
        <Row
          label={ahead ? `Undercut P${ahead.place} ${name(ahead)}` : "Undercut (ahead)"}
          gap={gapAhead}
          pitLoss={pitLossSeconds}
          enabled={!!ahead && gapAhead > 0}
        />
        <Row
          label={behind ? `Defend vs P${behind.place} ${name(behind)}` : "Defend (behind)"}
          gap={gapBehind}
          pitLoss={pitLossSeconds}
          enabled={!!behind && gapBehind > 0}
          defend
        />
      </div>
    </section>
  );
}

function Row({ label, gap, pitLoss, enabled, defend }: { label: string; gap: number; pitLoss: number; enabled: boolean; defend?: boolean }) {
  const toFind = pitLoss - gap; // seconds of pace advantage needed for the (under/over)cut to work
  let verdict: string;
  if (!enabled) {
    verdict = "—";
  } else if (toFind <= 0) {
    verdict = defend ? "⚠ exposed" : "✓ track position";
  } else {
    verdict = `${toFind.toFixed(1)}s to find`;
  }
  return (
    <div className="strat-readout">
      <span>{label}</span>
      <strong>{verdict}</strong>
      {enabled && <small>gap {gap.toFixed(1)}s</small>}
    </div>
  );
}

function name(c: { driver: string; car_name: string }): string {
  return c.driver || c.car_name || "";
}
