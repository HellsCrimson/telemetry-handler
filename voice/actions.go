package voice

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"telemetry-handler/game/lmu/rest"
)

// Controller is the subset of the LMU REST client the executor needs. *rest.Client
// satisfies it; tests use a fake. Kept narrow so the voice package depends only on
// the pit-menu read/write surface, not the whole client.
type Controller interface {
	PitMenu(ctx context.Context) ([]rest.PitMenuItem, error)
	SetPitMenuValue(ctx context.Context, pmc, setting int) error
}

// PitWrite is one resolved pit-menu change: select option Setting on component PMC.
// Name/Label are carried only for the human-readable description.
type PitWrite struct {
	PMC     int
	Setting int
	Name    string
	Label   string
}

// Plan is the set of pit-menu writes resolved from one Utterance, plus a short
// description shown on the overlay while awaiting confirmation. Writes is empty
// when nothing in the utterance mapped to an available pit-menu component.
type Plan struct {
	Writes []PitWrite
	Desc   string
}

// Important reports whether the plan needs confirmation (any pit change does).
func (p Plan) Important() bool { return len(p.Writes) > 0 }

// Component-name and option-label match keywords, tuned against a live LMU pit
// menu (a hypercar: VIRTUAL ENERGY rather than litre fuel; TIRES + per-corner
// FL/FR/RL/RR TIRE components whose options are the compounds themselves —
// "No Change"/"New Medium"/"New Wet"/"Used Medium"/"Mixed"). The exact strings
// are not contractually fixed, so matching is by case-insensitive substring and
// centralized here. Use `-voice-menu` to dump a car's real menu when tuning.
const (
	fuelNameMatch   = "FUEL"   // litre fuel; excludes "FUEL RATIO" (see findFuelComponent)
	energyNameMatch = "ENERGY" // VIRTUAL ENERGY (hypercars)
	noChangeMatch   = "NO CHANGE"
	newTyreMatch    = "NEW"
)

var labelNumRe = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

// Resolve builds a Plan from parsed actions against the live pit menu. It fetches
// the menu once, maps each action to a component+option, and returns the writes
// plus a description. An action that can't be mapped is skipped and noted in the
// description so the driver sees what was (and wasn't) understood.
func Resolve(ctx context.Context, c Controller, actions []Action) (Plan, error) {
	items, err := c.PitMenu(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("read pit menu: %w", err)
	}
	var plan Plan
	var parts, misses []string
	for _, a := range actions {
		writes, label, ok := resolveAction(items, a)
		if !ok {
			misses = append(misses, actionWord(a))
			continue
		}
		plan.Writes = append(plan.Writes, writes...)
		parts = append(parts, label)
	}
	plan.Desc = strings.Join(parts, ", ")
	if len(misses) > 0 {
		if plan.Desc != "" {
			plan.Desc += " "
		}
		plan.Desc += "(no " + strings.Join(misses, "/") + ")"
	}
	return plan, nil
}

func resolveAction(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	switch a.Type {
	case ActFuel:
		return resolveFuel(items, a)
	case ActEnergy:
		return resolveEnergy(items, a)
	case ActTyres:
		return resolveTyres(items, a)
	case ActPressure:
		return resolvePressure(items, a)
	case ActBrakeDuct:
		return resolveBrakeDuct(items, a)
	case ActBrakes:
		return resolveBrakes(items, a)
	case ActWing:
		return resolveWing(items, a)
	case ActGrille:
		return resolveGrille(items, a)
	case ActDamage:
		return resolveDamage(items, a)
	case ActPit:
		return resolvePit(items, a)
	default:
		return nil, "", false
	}
}

// resolveFuel maps a fuel command onto a litre FUEL component (GT3/LMP2 cars).
// Hypercars have no such component — see resolveEnergy.
func resolveFuel(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	it, ok := findFuelComponent(items)
	if !ok || len(it.Settings) == 0 {
		return nil, "", false
	}
	var idx int
	switch {
	case a.FuelMax:
		idx = indexOfExtremeNumber(it.Settings, true)
	case a.FuelSet && a.Liters == 0:
		if i, ok := indexOfKeyword(it.Settings, noChangeMatch); ok {
			idx = i
		} else {
			idx = indexOfExtremeNumber(it.Settings, false)
		}
	default:
		idx = indexOfClosestNumber(it.Settings, a.Liters)
	}
	return []PitWrite{pitWrite(it, idx)}, "FUEL " + amountLabel(a, it, idx, "L"), true
}

// resolveEnergy maps an energy command onto the VIRTUAL ENERGY component, whose
// options are "<pct>% <n> laps"; the value is matched on the leading percentage.
func resolveEnergy(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	it, ok := findItem(items, energyNameMatch)
	if !ok || len(it.Settings) == 0 {
		return nil, "", false
	}
	var idx int
	switch {
	case a.FuelMax:
		idx = indexOfExtremeNumber(it.Settings, true)
	case a.FuelSet && a.Liters == 0:
		idx = indexOfExtremeNumber(it.Settings, false)
	default:
		idx = indexOfClosestNumber(it.Settings, a.Liters)
	}
	return []PitWrite{pitWrite(it, idx)}, "ENERGY " + amountLabel(a, it, idx, "%"), true
}

// resolveTyres maps a tyre command onto LMU's tyre components. The actual choice
// lives in the per-corner "FL/FR/RL/RR TIRE" components — the aggregate "TIRES"
// entry is only a summary that reads "Mixed" when the corners disagree, so
// writing it alone does NOT change the tyres. We therefore write the relevant
// corners directly (all four / front pair / rear pair), falling back to the
// aggregate only if a car exposes no per-corner components. The option list IS
// the compound choice ("No Change"/"New Medium"/"New Wet"/…); a compound (or the
// default first "New") picks the option, and the selection picks which corners.
func resolveTyres(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	var comps []rest.PitMenuItem
	var which string
	switch a.Which {
	case TyreFront:
		comps, which = findCornerTyres(items, "FL", "FR"), "FRONT TYRES"
	case TyreRear:
		comps, which = findCornerTyres(items, "RL", "RR"), "REAR TYRES"
	default: // TyreAll or a bare compound (TyreUnset)
		comps, which = findCornerTyres(items, "FL", "FR", "RL", "RR"), "ALL TYRES"
	}
	// Fall back to the aggregate component on cars that have no per-corner ones.
	if len(comps) == 0 {
		if it, ok := findAggregateTyre(items); ok {
			comps = []rest.PitMenuItem{it}
		}
	}
	if len(comps) == 0 {
		return nil, "", false
	}

	var idx int
	var label string
	if a.NoTyres {
		i, ok := indexOfKeyword(comps[0].Settings, noChangeMatch)
		if !ok {
			return nil, "", false
		}
		idx, which = i, "NO TYRES"
	} else {
		i, l, ok := chooseTyreOption(comps[0].Settings, a.Compound)
		if !ok {
			return nil, "", false
		}
		idx, label = i, l
	}

	writes := make([]PitWrite, 0, len(comps))
	for _, c := range comps {
		writes = append(writes, pitWrite(c, idx))
	}
	if label != "" {
		which += " " + cleanLabel(label)
	}
	return writes, which, true
}

// resolvePit currently finds nothing: LMU has no pit-request component in the
// pit menu (it is a separate control). Kept so "box this lap" degrades to a
// clear "no PIT" miss rather than a silent no-op.
func resolvePit(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	it, ok := findItem(items, "PIT REQUEST", "REQUEST PIT")
	if !ok || len(it.Settings) == 0 {
		return nil, "", false
	}
	var idx int
	var word string
	if a.PitOn {
		if i, ok := indexOfKeyword(it.Settings, "YES|REQUEST|ON|BOX"); ok {
			idx, word = i, "PIT"
		} else {
			idx, word = len(it.Settings)-1, "PIT"
		}
	} else {
		if i, ok := indexOfKeyword(it.Settings, "NO|NONE|OFF|CANCEL"); ok {
			idx, word = i, "CANCEL PIT"
		} else {
			idx, word = 0, "CANCEL PIT"
		}
	}
	return []PitWrite{pitWrite(it, idx)}, word, true
}

// resolvePressure sets tyre pressures on the relevant corners' PRESS components.
func resolvePressure(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	var prefixes []string
	var which string
	switch a.Which {
	case TyreFront:
		prefixes, which = []string{"FL", "FR"}, "FRONT PRESSURE"
	case TyreRear:
		prefixes, which = []string{"RL", "RR"}, "REAR PRESSURE"
	default:
		prefixes, which = []string{"FL", "FR", "RL", "RR"}, "ALL PRESSURE"
	}
	comps := findSuffixComponents(items, "PRESS", prefixes...)
	if len(comps) == 0 {
		return nil, "", false
	}
	idx := chooseIndex(comps[0], a)
	writes := make([]PitWrite, 0, len(comps))
	for _, c := range comps {
		writes = append(writes, pitWrite(c, idx))
	}
	return writes, which + " " + cleanLabel(settingLabel(comps[0], idx)), true
}

// resolveBrakeDuct sets brake-duct opening on the front and/or rear duct.
func resolveBrakeDuct(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	var prefixes []string
	var which string
	switch a.Which {
	case TyreFront:
		prefixes, which = []string{"F"}, "FRONT DUCT"
	case TyreRear:
		prefixes, which = []string{"R"}, "REAR DUCT"
	default:
		prefixes, which = []string{"F", "R"}, "DUCTS"
	}
	comps := findDuctComponents(items, prefixes...)
	if len(comps) == 0 {
		return nil, "", false
	}
	writes := make([]PitWrite, 0, len(comps))
	for _, c := range comps {
		var idx int
		switch {
		case a.Open:
			if i, ok := indexOfKeyword(c.Settings, "OPEN"); ok {
				idx = i
			}
		case a.Closed:
			if i, ok := indexOfKeyword(c.Settings, "CLOSED|SHUT"); ok {
				idx = i
			} else {
				idx = len(c.Settings) - 1
			}
		default:
			idx = chooseIndex(c, a)
		}
		writes = append(writes, pitWrite(c, idx))
	}
	return writes, which + " " + cleanLabel(settingLabel(comps[0], writes[0].Setting)), true
}

// resolveBrakes toggles the REPLACE BRAKES component.
func resolveBrakes(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	it, ok := findItem(items, "REPLACE BRAKE")
	if !ok || len(it.Settings) == 0 {
		return nil, "", false
	}
	key, word := "NO|NONE", "KEEP BRAKES"
	if a.On {
		key, word = "YES", "REPLACE BRAKES"
	}
	i, ok := indexOfKeyword(it.Settings, key)
	if !ok {
		return nil, "", false
	}
	return []PitWrite{pitWrite(it, i)}, word, true
}

// resolveWing sets the rear-wing angle (absolute degrees or a relative delta).
func resolveWing(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	it, ok := findItem(items, "R WING", "REAR WING", "WING")
	if !ok || len(it.Settings) == 0 {
		return nil, "", false
	}
	idx := chooseIndex(it, a)
	return []PitWrite{pitWrite(it, idx)}, "WING " + cleanLabel(settingLabel(it, idx)), true
}

// resolveGrille sets the radiator grille/tape level (absolute or relative).
func resolveGrille(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	it, ok := findItem(items, "GRILLE", "GRILL", "RADIATOR", "TAPE")
	if !ok || len(it.Settings) == 0 {
		return nil, "", false
	}
	idx := chooseIndex(it, a)
	return []PitWrite{pitWrite(it, idx)}, "GRILLE " + cleanLabel(settingLabel(it, idx)), true
}

// resolveDamage toggles damage repair. With nothing repairable (only an "N/A"
// option) it reports a miss rather than a no-op write.
func resolveDamage(items []rest.PitMenuItem, a Action) ([]PitWrite, string, bool) {
	it, ok := findItem(items, "DAMAGE")
	if !ok || len(it.Settings) == 0 {
		return nil, "", false
	}
	if !a.On {
		if i, ok := indexOfKeyword(it.Settings, "NO|NONE"); ok {
			return []PitWrite{pitWrite(it, i)}, "NO REPAIR", true
		}
		return []PitWrite{pitWrite(it, 0)}, "NO REPAIR", true
	}
	if i, ok := indexOfKeyword(it.Settings, "ALL|FULL|YES|REPAIR"); ok {
		return []PitWrite{pitWrite(it, i)}, "REPAIR " + cleanLabel(settingLabel(it, i)), true
	}
	if len(it.Settings) > 1 {
		i := len(it.Settings) - 1
		return []PitWrite{pitWrite(it, i)}, "REPAIR " + cleanLabel(settingLabel(it, i)), true
	}
	return nil, "", false // only "N/A" — nothing to repair
}

// chooseIndex resolves an option index from an action's numeric value: a relative
// click delta off the current selection, an absolute closest-number match, else
// the current selection unchanged.
func chooseIndex(it rest.PitMenuItem, a Action) int {
	switch {
	case a.DeltaSet:
		return clampIdx(it.CurrentSetting+a.Delta, len(it.Settings))
	case a.ValueSet:
		return indexOfClosestNumber(it.Settings, a.Value)
	default:
		return clampIdx(it.CurrentSetting, len(it.Settings))
	}
}

func clampIdx(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// findSuffixComponents returns components named "<prefix> <suffix>" (e.g.
// "FL PRESS") for each prefix, in prefix order.
func findSuffixComponents(items []rest.PitMenuItem, suffix string, prefixes ...string) []rest.PitMenuItem {
	var out []rest.PitMenuItem
	for _, p := range prefixes {
		for _, it := range items {
			up := strings.ToUpper(it.Name)
			if strings.HasPrefix(up, p+" ") && strings.Contains(up, suffix) {
				out = append(out, it)
			}
		}
	}
	return out
}

// findDuctComponents returns the brake-duct components for the given side
// prefixes ("F"/"R"), matching names like "F BRAKE DUCT".
func findDuctComponents(items []rest.PitMenuItem, prefixes ...string) []rest.PitMenuItem {
	var out []rest.PitMenuItem
	for _, p := range prefixes {
		for _, it := range items {
			up := strings.ToUpper(it.Name)
			if strings.HasPrefix(up, p+" ") && strings.Contains(up, "DUCT") {
				out = append(out, it)
			}
		}
	}
	return out
}

// Apply writes every change in the plan, stopping at the first error.
func (p Plan) Apply(ctx context.Context, c Controller) error {
	for _, w := range p.Writes {
		if err := c.SetPitMenuValue(ctx, w.PMC, w.Setting); err != nil {
			return fmt.Errorf("set %s: %w", w.Name, err)
		}
	}
	return nil
}

// --- component finders ---

// findItem returns the first menu item whose (upper-cased) name contains any of
// the given substrings.
func findItem(items []rest.PitMenuItem, subs ...string) (rest.PitMenuItem, bool) {
	for _, it := range items {
		name := strings.ToUpper(it.Name)
		for _, s := range subs {
			if strings.Contains(name, strings.ToUpper(s)) {
				return it, true
			}
		}
	}
	return rest.PitMenuItem{}, false
}

// findFuelComponent returns the litre-fuel component, excluding "FUEL RATIO"
// (the energy/fuel mix setting, not a fuel amount).
func findFuelComponent(items []rest.PitMenuItem) (rest.PitMenuItem, bool) {
	for _, it := range items {
		up := strings.ToUpper(it.Name)
		if strings.Contains(up, fuelNameMatch) && !strings.Contains(up, "RATIO") {
			return it, true
		}
	}
	return rest.PitMenuItem{}, false
}

// findAggregateTyre returns the all-corners tyre component ("TIRES"/"TYRES",
// plural), as distinct from the per-corner "FL TIRE" etc.
func findAggregateTyre(items []rest.PitMenuItem) (rest.PitMenuItem, bool) {
	return findItem(items, "TIRES", "TYRES")
}

// findCornerTyres returns the per-corner tyre components whose names start with
// the given corner prefixes (e.g. "FL", "FR").
func findCornerTyres(items []rest.PitMenuItem, prefixes ...string) []rest.PitMenuItem {
	var out []rest.PitMenuItem
	for _, it := range items {
		up := strings.ToUpper(it.Name)
		for _, p := range prefixes {
			if strings.HasPrefix(up, p+" TIRE") || strings.HasPrefix(up, p+" TYRE") {
				out = append(out, it)
			}
		}
	}
	return out
}

// --- option selection ---

// chooseTyreOption picks a tyre option index: the one matching the requested
// compound, else the first "New" option (a plain "change tyres" with no compound,
// or a compound the car doesn't offer). Returns the chosen option label too.
func chooseTyreOption(settings []string, compound string) (int, string, bool) {
	if compound != "" {
		if i, ok := indexOfKeyword(settings, strings.ToUpper(compound)); ok {
			return i, settings[i], true
		}
	}
	if i, ok := indexOfKeyword(settings, newTyreMatch); ok {
		return i, settings[i], true
	}
	return 0, "", false
}

// indexOfKeyword returns the index of the first option label containing any of
// the |-separated keywords (case-insensitive).
func indexOfKeyword(settings []string, keywords string) (int, bool) {
	keys := strings.Split(strings.ToUpper(keywords), "|")
	for i, s := range settings {
		up := strings.ToUpper(s)
		for _, k := range keys {
			if k != "" && strings.Contains(up, k) {
				return i, true
			}
		}
	}
	return 0, false
}

// indexOfClosestNumber returns the option index whose parsed leading number is
// closest to target. Options with no number are ignored.
func indexOfClosestNumber(settings []string, target float64) int {
	best, bestDiff, found := 0, 0.0, false
	for i, s := range settings {
		n, ok := parseLeadingNumber(s)
		if !ok {
			continue
		}
		diff := n - target
		if diff < 0 {
			diff = -diff
		}
		if !found || diff < bestDiff {
			best, bestDiff, found = i, diff, true
		}
	}
	return best
}

// indexOfExtremeNumber returns the option index with the highest (max=true) or
// lowest numeric value.
func indexOfExtremeNumber(settings []string, max bool) int {
	best, bestVal, found := 0, 0.0, false
	for i, s := range settings {
		n, ok := parseLeadingNumber(s)
		if !ok {
			continue
		}
		if !found || (max && n > bestVal) || (!max && n < bestVal) {
			best, bestVal, found = i, n, true
		}
	}
	return best
}

func parseLeadingNumber(s string) (float64, bool) {
	m := labelNumRe.FindString(s)
	if m == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func settingLabel(it rest.PitMenuItem, idx int) string {
	if idx >= 0 && idx < len(it.Settings) {
		return it.Settings[idx]
	}
	return ""
}

// amountLabel renders a fuel/energy amount for the description, preferring the
// resolved option's number with the given unit, else the requested value.
func amountLabel(a Action, it rest.PitMenuItem, idx int, unit string) string {
	if a.FuelMax {
		return "MAX"
	}
	if n, ok := parseLeadingNumber(settingLabel(it, idx)); ok {
		return fmt.Sprintf("%g%s", n, unit)
	}
	if a.FuelSet {
		return fmt.Sprintf("%g%s", a.Liters, unit)
	}
	return ""
}

func pitWrite(it rest.PitMenuItem, idx int) PitWrite {
	return PitWrite{PMC: it.PMCValue, Setting: idx, Name: it.Name, Label: settingLabel(it, idx)}
}

// cleanLabel tidies an option label for the description (trim, collapse spaces,
// upper-case): "New Medium " -> "NEW MEDIUM".
func cleanLabel(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(s), " "))
}

func actionWord(a Action) string {
	switch a.Type {
	case ActFuel:
		return "FUEL"
	case ActEnergy:
		return "ENERGY"
	case ActTyres:
		return "TYRES"
	case ActPressure:
		return "PRESSURE"
	case ActBrakeDuct:
		return "DUCT"
	case ActBrakes:
		return "BRAKES"
	case ActWing:
		return "WING"
	case ActGrille:
		return "GRILLE"
	case ActDamage:
		return "DAMAGE"
	case ActPit:
		return "PIT"
	default:
		return "?"
	}
}
