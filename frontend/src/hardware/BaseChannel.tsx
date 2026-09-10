// The base channel — the one place in the app that writes the wheelbase's own
// memory.
//
// This exists because two Applies now coexist and mean entirely different
// things. The header's Save writes THE APP's config file: LED colours, the
// pinned port, brightness. This strip writes THE BASE's stored settings, which
// persist on the hardware, survive a reboot, and are still there next time the
// user drives even if this app is gone.
//
// They are kept apart by four things at once, because a label alone is thin:
// a darker ground with its own device gutter down the left, a line stating what
// the channel writes, an outlined mono button rather than the header's filled
// pill, and amber for its staged count where the header's is ink grey. Amber on
// this page means exactly one thing — not yet on the wheel.

interface Props {
  online: boolean;
  name: string;
  meta: string;
  /** Names of the settings staged, for the chip. */
  staged: string[];
  applying: boolean;
  reading: boolean;
  onWrite: () => void;
  onRevert: () => void;
  onReRead: () => void;
  /** Set when the config forbids writing; the button says so instead of lying. */
  blocked?: string;
}

export default function BaseChannel({
  online,
  name,
  meta,
  staged,
  applying,
  reading,
  onWrite,
  onRevert,
  onReRead,
  blocked,
}: Props) {
  const n = staged.length;

  return (
    <div className={`base-strip${online ? "" : " offline"}`}>
      <div style={{ display: "flex", flexDirection: "column", gap: 2, minWidth: 0 }}>
        <div className="who">
          <span className="dot" />
          {name}
          <span className="meta">{meta}</span>
        </div>
        <div className="channel">
          {online
            ? "→ THIS STRIP WRITES THE BASE'S OWN MEMORY · SURVIVES RESTARTS AND REBOOTS"
            : "→ NOTHING TO WRITE TO · THE APP KEEPS LOOKING"}
        </div>
      </div>

      <span style={{ flex: 1 }} />

      {n > 0 && (
        <span className="staged-chip">
          <span className="mark" />
          {n} STAGED · {summarise(staged)}
        </span>
      )}

      {n > 0 && (
        <button className="btn" onClick={onRevert} disabled={applying}>
          Revert
        </button>
      )}
      <button className="btn" onClick={onReRead} disabled={!online || reading || applying}>
        {reading ? "Reading…" : "Re-read from base"}
      </button>
      <button
        className="btn-write"
        onClick={onWrite}
        disabled={n === 0 || applying || !online || !!blocked}
        title={blocked || undefined}
      >
        <span>{applying ? "WRITING…" : blocked ? "WRITING DISABLED" : `WRITE ${n} TO BASE`}</span>
        {!blocked && <span style={{ font: "400 12px var(--font-mono)", color: "#8a94a3" }}>→</span>}
      </button>
    </div>
  );
}

/** The chip names what is staged rather than only counting it — "2 staged" tells
 *  you to look, "damping, soft limit" tells you whether you need to. */
function summarise(names: string[]): string {
  const shown = names.slice(0, 2).map((n) => n.toUpperCase());
  return names.length > 2 ? `${shown.join(", ")} +${names.length - 2}` : shown.join(", ");
}
