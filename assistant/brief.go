// Package assistant is the LLM race engineer: it turns the live session state
// into a compact briefing, asks a local model, and answers the driver over the
// voice channel. Pit changes it proposes are expressed in the existing voice
// grammar, so they flow through the same resolve → confirm → apply path as a
// spoken command and nothing reaches the car unconfirmed.
package assistant

import (
	"fmt"
	"sort"
	"strings"

	"telemetry-handler/engineer"
)

const (
	// briefCars is how many rivals either side of the player are described. A full
	// grid would dominate the prompt, and a driver asks about the cars they are
	// actually racing.
	briefCars = 3
	// briefForecast is how many forecast points are worth carrying. The driver
	// cares what the weather does next, not at the end of a 24-hour race.
	briefForecast = 3
	// briefSectorLosses is how many of the worst mini-sectors are listed. Three is
	// what a driver can actually act on in the next lap.
	briefSectorLosses = 3
	// sectorLossFloor is the smallest time loss worth mentioning (seconds).
	sectorLossFloor = 0.05
)

// Brief renders the session into the compact context the model reasons over.
//
// This function, more than the prompt or the model, decides whether the answers
// are any good: anything it omits the engineer simply cannot know, and anything
// it pads costs time to first word. It is written as terse labelled lines rather
// than JSON because that costs roughly half the tokens for the same content.
func Brief(st engineer.SessionState) string {
	if !st.Available {
		return "No live session. Telemetry is not being received."
	}
	var b strings.Builder
	player := playerCar(st)

	fmt.Fprintf(&b, "TRACK: %s", orUnknown(st.Track))
	if st.TrackLength > 0 {
		fmt.Fprintf(&b, " (%.2f km)", st.TrackLength/1000)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "SESSION: %s, elapsed %s", sessionKind(st.SessionType), clock(st.SessionTime))
	switch {
	case st.MaxLaps > 0 && player != nil:
		fmt.Fprintf(&b, ", lap %d of %d", player.TotalLaps+1, st.MaxLaps)
	case st.SessionEndTime > st.SessionTime:
		fmt.Fprintf(&b, ", %s remaining", clock(st.SessionEndTime-st.SessionTime))
	}
	b.WriteString("\n")

	if f := flags(st.Flags); f != "" {
		fmt.Fprintf(&b, "FLAGS: %s\n", f)
	}
	fmt.Fprintf(&b, "WEATHER: air %.0fC, track %.0fC, rain %.0f%%, cloud %.0f%%\n",
		st.Weather.AmbientTemp, st.Weather.TrackTemp, st.Weather.Raining*100, st.Weather.Cloudiness*100)

	if player != nil {
		writePlayer(&b, st, *player)
	}
	writeRivals(&b, st, player)

	if st.Strategy.Present {
		if e := st.Strategy.PitEstimate; e.Total > 0 {
			fmt.Fprintf(&b, "PIT STOP: about %.0fs total\n", e.Total)
		}
		writePitMenu(&b, st)
		writeForecast(&b, st)
	}
	if player != nil {
		writeSectorLosses(&b, st, *player)
	}
	if bal := st.Player.Balance; bal.Verdict != "" {
		fmt.Fprintf(&b, "BALANCE: %s", bal.Verdict)
		if bal.Proposal != "" {
			fmt.Fprintf(&b, " — %s", bal.Proposal)
		}
		b.WriteString("\n")
	}
	writeEvents(&b, st)
	return b.String()
}

func writePlayer(b *strings.Builder, st engineer.SessionState, p engineer.CarState) {
	fmt.Fprintf(b, "YOU: P%d", p.Place)
	if p.CarName != "" {
		fmt.Fprintf(b, " in the %s", p.CarName)
	}
	fmt.Fprintf(b, ", %d laps done", p.TotalLaps)
	if p.InPits {
		b.WriteString(", IN THE PITS")
	}
	fmt.Fprintf(b, ", %s\n", plural(p.NumPitstops, "stop"))

	if p.LastLap > 0 || p.BestLap > 0 {
		fmt.Fprintf(b, "LAPS: last %s, best %s\n", lap(p.LastLap), lap(p.BestLap))
	}

	fuel := fmt.Sprintf("FUEL: %.1f L", p.Fuel)
	if capacity := fuelCapacity(st, p); capacity > 0 {
		fuel += fmt.Sprintf(" of %.0f L", capacity)
	}
	if laps, per, ok := fuelRange(st, p); ok {
		fuel += fmt.Sprintf(", about %.1f laps left at %.2f L/lap", laps, per)
	}
	b.WriteString(fuel + "\n")

	if p.Battery > 0 {
		fmt.Fprintf(b, "BATTERY: %.0f%%\n", p.Battery*100)
	}
	fmt.Fprintf(b, "TYRES: %s\n", tyres(p))
	if d := st.Player.WorstDent; d > 0 {
		fmt.Fprintf(b, "DAMAGE: worst panel severity %d of 2\n", d)
	}
}

// writePitMenu lists what is currently staged for the next stop. Without it the
// engineer proposes changes blind — it cannot say what is already set, and it
// re-requests things that are already selected.
func writePitMenu(b *strings.Builder, st engineer.SessionState) {
	if len(st.Strategy.PitMenu) == 0 {
		return
	}
	parts := make([]string, 0, len(st.Strategy.PitMenu))
	for _, e := range st.Strategy.PitMenu {
		if strings.TrimSpace(e.Current) == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s", strings.TrimSpace(e.Name), strings.TrimSpace(e.Current)))
	}
	if len(parts) > 0 {
		fmt.Fprintf(b, "PIT MENU NOW: %s\n", strings.Join(parts, "; "))
	}
}

// writeForecast carries the next few weather nodes, which is what a strategy
// question ("do I need wets?") actually turns on.
func writeForecast(b *strings.Builder, st engineer.SessionState) {
	if len(st.Strategy.Forecast) == 0 {
		return
	}
	points := st.Strategy.Forecast
	if len(points) > briefForecast {
		points = points[:briefForecast]
	}
	parts := make([]string, 0, len(points))
	for _, f := range points {
		parts = append(parts, fmt.Sprintf("%s %.0f%% rain %.0fC %s",
			shortNode(f.Node), f.RainChance, f.Temperature, strings.ToLower(f.Sky)))
	}
	fmt.Fprintf(b, "FORECAST: %s\n", strings.Join(parts, "; "))
}

// writeSectorLosses names where the last lap gave time away against the driver's
// own best, which is the question behind "how was that lap?".
func writeSectorLosses(b *strings.Builder, st engineer.SessionState, p engineer.CarState) {
	type loss struct {
		idx   int
		delta float64
	}
	var losses []loss
	for i := range p.MiniSectors {
		if i >= len(p.BestSectors) {
			break
		}
		last, best := p.MiniSectors[i].TimeSpent, p.BestSectors[i].TimeSpent
		if last <= 0 || best <= 0 {
			continue
		}
		if d := last - best; d >= sectorLossFloor {
			losses = append(losses, loss{i, d})
		}
	}
	if len(losses) == 0 {
		return
	}
	sort.Slice(losses, func(i, j int) bool { return losses[i].delta > losses[j].delta })
	if len(losses) > briefSectorLosses {
		losses = losses[:briefSectorLosses]
	}
	parts := make([]string, 0, len(losses))
	for _, l := range losses {
		parts = append(parts, fmt.Sprintf("%s +%.2fs", cornerName(st, l.idx), l.delta))
	}
	fmt.Fprintf(b, "LAST LAP LOST vs your best: %s\n", strings.Join(parts, ", "))
}

// cornerName labels a mini-sector with the derived corner name when there is
// one, else its index — "T4" means more to a driver than "sector 9".
func cornerName(st engineer.SessionState, idx int) string {
	if idx < len(st.Corners) && strings.TrimSpace(st.Corners[idx]) != "" {
		return st.Corners[idx]
	}
	return fmt.Sprintf("sector %d", idx+1)
}

// shortNode trims LMU's forecast node labels ("NODE_25") to something speakable.
func shortNode(node string) string {
	n := strings.TrimPrefix(strings.TrimSpace(node), "NODE_")
	if n == "" {
		return "next"
	}
	return strings.ToLower(n)
}

// writeRivals describes only the cars the driver is actually racing — a few
// ahead and a few behind — with the gaps that decide strategy.
func writeRivals(b *strings.Builder, st engineer.SessionState, player *engineer.CarState) {
	if player == nil || len(st.Cars) == 0 {
		return
	}
	ahead, behind := neighbours(st, *player)
	if len(ahead) > 0 {
		b.WriteString("AHEAD:\n")
		for i := len(ahead) - 1; i >= 0; i-- {
			writeRival(b, ahead[i], gapBetween(ahead[i], *player))
		}
	}
	if len(behind) > 0 {
		b.WriteString("BEHIND:\n")
		for _, c := range behind {
			writeRival(b, c, gapBetween(*player, c))
		}
	}
}

func writeRival(b *strings.Builder, c engineer.CarState, gap float64) {
	fmt.Fprintf(b, "  P%d %s", c.Place, orUnknown(c.Driver))
	if c.Class != "" {
		fmt.Fprintf(b, " (%s)", c.Class)
	}
	if gap != 0 {
		fmt.Fprintf(b, ", %.1fs", abs(gap))
	}
	fmt.Fprintf(b, ", %s", plural(c.NumPitstops, "stop"))
	if c.InPits {
		b.WriteString(", in the pits")
	}
	if c.LastLap > 0 {
		fmt.Fprintf(b, ", last %s", lap(c.LastLap))
	}
	b.WriteString("\n")
}

// writeEvents lists the tail of the race timeline, which is how the model knows
// about things that already happened (flags, stops, contact).
func writeEvents(b *strings.Builder, st engineer.SessionState) {
	const maxEvents = 5
	if len(st.Events) == 0 {
		return
	}
	start := max(0, len(st.Events)-maxEvents)
	b.WriteString("RECENT:\n")
	for _, e := range st.Events[start:] {
		fmt.Fprintf(b, "  [%s] %s\n", clock(e.AtET), e.Message)
	}
}

// --- helpers ---------------------------------------------------------------

func playerCar(st engineer.SessionState) *engineer.CarState {
	for i := range st.Cars {
		if st.Cars[i].IsPlayer {
			return &st.Cars[i]
		}
	}
	return nil
}

// neighbours returns the cars immediately ahead of and behind the player on the
// road, nearest first.
func neighbours(st engineer.SessionState, player engineer.CarState) (ahead, behind []engineer.CarState) {
	for _, c := range st.Cars {
		if c.ID == player.ID {
			continue
		}
		switch {
		case c.Place < player.Place && player.Place-c.Place <= briefCars:
			ahead = append(ahead, c)
		case c.Place > player.Place && c.Place-player.Place <= briefCars:
			behind = append(behind, c)
		}
	}
	return ahead, behind
}

// gapBetween approximates the interval between two cars from their gaps to the
// leader, which is the only interval LMU reports for arbitrary pairs.
func gapBetween(front, back engineer.CarState) float64 {
	return back.GapToLeader - front.GapToLeader
}

func fuelCapacity(st engineer.SessionState, p engineer.CarState) float64 {
	if st.Strategy.Present && st.Strategy.FuelCapacity > 0 {
		return st.Strategy.FuelCapacity
	}
	return p.FuelCapacity
}

// fuelRange estimates laps remaining from the fuel burned over the last complete
// lap, which is what the mini-sector accumulator already measures.
//
// It reports ok=false unless that lap was captured end to end. A lap the app
// only saw part of — it started mid-lap, or the car came out of the pits — burns
// less than a lap's worth of fuel while still looking like a completed lap, and
// dividing by it invents range out of nowhere. A wrong fuel figure is the one
// number an engineer must never say, so a partial lap yields no estimate at all
// rather than a confident wrong one.
func fuelRange(_ engineer.SessionState, p engineer.CarState) (laps, perLap float64, ok bool) {
	if len(p.MiniSectors) == 0 {
		return 0, 0, false
	}
	for _, ms := range p.MiniSectors {
		if ms.TimeSpent <= 0 {
			return 0, 0, false // this slice of the lap was never driven under observation
		}
		perLap += ms.FuelUsed
	}
	if perLap <= 0 || p.Fuel <= 0 {
		return 0, 0, false
	}
	return p.Fuel / perLap, perLap, true
}

func tyres(p engineer.CarState) string {
	names := [4]string{"FL", "FR", "RL", "RR"}
	parts := make([]string, 0, 4)
	for i, t := range p.Tires {
		// Wear is reported as a fraction where 1 is fresh, so wear used is 1-w.
		parts = append(parts, fmt.Sprintf("%s %.0f%% worn %.0fC", names[i], (1-t.Wear)*100, t.Temp[1]))
	}
	s := strings.Join(parts, ", ")
	if c := p.Tires[0].Compound; c != "" {
		s = c + ": " + s
	}
	return s
}

func flags(f engineer.FlagState) string {
	switch {
	case f.SCActive:
		return "SAFETY CAR DEPLOYED"
	case f.Yellow:
		return "YELLOW"
	case f.Green:
		return "green"
	}
	return ""
}

func sessionKind(t int32) string {
	switch {
	case t == 0:
		return "test day"
	case t >= 1 && t <= 4:
		return "practice"
	case t >= 5 && t <= 8:
		return "qualifying"
	case t == 9:
		return "warmup"
	case t >= 10:
		return "race"
	}
	return "session"
}

// clock renders seconds as h:mm:ss / m:ss.
func clock(sec float64) string {
	if sec <= 0 {
		return "0:00"
	}
	total := int(sec)
	if h := total / 3600; h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, (total%3600)/60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// lap renders a lap time as m:ss.sss.
func lap(sec float64) string {
	if sec <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d:%06.3f", int(sec)/60, sec-float64(int(sec)/60*60))
}

// plural renders a count with its noun, so the briefing does not read "1 stops"
// to a model that is being asked to speak naturally.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
