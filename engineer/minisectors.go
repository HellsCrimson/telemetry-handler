package engineer

// numMiniSectors is how many equal slices we cut each lap into for the per-corner
// analysis. LMU exposes no corner IDs — only distance-along-the-lap — so we
// segment purely by distance. 20 is a pragmatic default (roughly one slice per
// significant corner/straight on most circuits) and is the single knob to tune.
//
// Upgrade path: a later phase can map human corner names ("Mulsanne", "Porsche
// Curves") onto contiguous runs of mini-sectors via a per-track config file,
// without changing any of the accumulation below.
const numMiniSectors = 20

// miniSectorIndex maps a 0..1 lap fraction to a mini-sector index in
// [0, numMiniSectors). Out-of-range fractions are clamped so a stray reading
// can't index past the array.
func miniSectorIndex(frac float64) int {
	idx := int(frac * numMiniSectors)
	if idx < 0 {
		return 0
	}
	if idx >= numMiniSectors {
		return numMiniSectors - 1
	}
	return idx
}
