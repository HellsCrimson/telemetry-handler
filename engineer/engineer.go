package engineer

import (
	"sync"

	"telemetry-handler/lmu/wire"
)

// Engineer is the live strategy engine. It observes every telemetry frame the
// receiver decodes and keeps the latest game-agnostic SessionState ready for the
// frontend to poll.
//
// It is the strategy counterpart to the overlay (another real-time per-frame
// consumer) and the analysis package (the same lap-segmentation idea, but run
// offline over a recording). Phase 1 only stores the latest mapped state; the
// stateful per-lap / per-mini-sector accumulation lands here in a later phase
// (Observe will then fold each frame into rolling per-car aggregates before
// storing the snapshot).
type Engineer struct {
	mu    sync.RWMutex
	state SessionState
}

// New returns an idle Engineer. Its Snapshot reports Available=false until the
// first frame is observed.
func New() *Engineer {
	return &Engineer{}
}

// Observe folds one decoded LMU frame into the engine's state. It is called once
// per frame from the receiver goroutine, at the sidecar's full rate — this is the
// only place every frame is seen, which is why per-corner accumulation must live
// here rather than in the 5 Hz frontend poll. A nil frame (e.g. the source
// switched to Forza) resets the engine to idle.
func (e *Engineer) Observe(f *wire.Frame) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f == nil {
		e.state = SessionState{}
		return
	}
	e.state = mapLMUFrame(f)
}

// Snapshot returns the latest SessionState for the frontend. It is a value copy
// of the current state (the Cars slice is shared, but Observe always replaces the
// slice wholesale rather than mutating it, so readers never see a torn frame).
func (e *Engineer) Snapshot() SessionState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}
