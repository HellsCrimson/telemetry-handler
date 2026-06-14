// EventTimeline is the console-style feed of race events (flags, pit entries,
// contacts) the engine generates from frame-to-frame transitions. It mirrors the
// Multi-viewer timeline: newest at the top, colour-coded by kind. It sits at the
// bottom of every strategy tab.
import { type RaceEvent, formatClock } from "../model";

export default function EventTimeline({ events }: { events: RaceEvent[] }) {
  if (!events || events.length === 0) {
    return (
      <aside className="strat-timeline">
        <h3>Timeline</h3>
        <p className="muted">No events yet.</p>
      </aside>
    );
  }
  // Newest first.
  const ordered = [...events].reverse();
  return (
    <aside className="strat-timeline">
      <h3>Timeline</h3>
      <ul>
        {ordered.map((e, i) => (
          <li key={i} className={`strat-event strat-event-${e.kind}`}>
            <span className="strat-event-time">{formatClock(e.at_et)}</span>
            <span className="strat-event-msg">{e.message}</span>
          </li>
        ))}
      </ul>
    </aside>
  );
}
