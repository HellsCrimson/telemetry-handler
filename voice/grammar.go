// Package voice is a local, offline voice-command MVP for driving LMU's pit
// strategy hands-free. A push-to-talk trigger (an external FIFO or a configured
// hardware/evdev button) records a short utterance, whisper.cpp transcribes it
// locally, a deterministic grammar (no LLM) turns the text into pit-menu
// actions, and — because pit changes are consequential mid-stint — the action is
// staged and shown on the overlay for a few seconds, executed only once the
// driver affirms ("yes"/"confirm"). The whole pipeline is host-local: no cloud,
// no network beyond LMU's own REST API on localhost.
//
// This file is the grammar: a pure transcript -> Utterance parser with no IO, so
// it is fully unit-testable. The fuzzy mapping from an Utterance to concrete LMU
// pit-menu writes lives in actions.go; the orchestration/confirmation state
// machine lives in engine.go.
package voice

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ActionType enumerates the pit-menu intents the MVP grammar understands. Every
// one mutates pit strategy, so all are treated as "important" (confirm-gated) —
// see Action.Important.
type ActionType int

const (
	// ActFuel sets the fuel to add at the next stop (a litre amount, or max/none).
	// Only present on cars with a litre fuel tank (GT3/LMP2…); hypercars use energy.
	ActFuel ActionType = iota
	// ActEnergy sets the virtual-energy target (a percentage, or max/none) on
	// hypercars, which have no litre fuel component.
	ActEnergy
	// ActTyres selects which tyres to change (and optionally the compound).
	ActTyres
	// ActPressure sets tyre pressures (front/rear/all) to an absolute value or a
	// relative click delta.
	ActPressure
	// ActBrakeDuct sets brake-duct opening (front/rear/all): open/closed or a %.
	ActBrakeDuct
	// ActBrakes toggles brake replacement (On = replace).
	ActBrakes
	// ActWing sets the rear-wing angle: absolute degrees or a relative click delta.
	ActWing
	// ActGrille sets the radiator grille/tape: absolute level or a relative delta.
	ActGrille
	// ActDamage toggles damage repair (On = repair).
	ActDamage
	// ActPit requests (or cancels) a pit stop — the "box this lap" call. Note LMU
	// has no pit-request entry in the pit menu, so this currently maps to nothing.
	ActPit
)

// TyreSel is which set of tyres a tyre action targets.
type TyreSel int

const (
	TyreUnset TyreSel = iota // not specified (e.g. a bare compound change)
	TyreAll
	TyreFront
	TyreRear
)

// Action is one parsed pit-strategy intent. Only the fields relevant to Type are
// populated; the executor (actions.go) maps it onto the live pit menu.
type Action struct {
	Type ActionType

	// Fuel (ActFuel).
	Liters  float64 // requested fuel when FuelSet
	FuelSet bool    // an explicit litre amount was given
	FuelMax bool    // "fill it up" / "full tank"

	// Tyres / pressures / brake ducts (ActTyres, ActPressure, ActBrakeDuct) share
	// the front/rear/all selection.
	Which    TyreSel
	NoTyres  bool   // "don't change tyres" / "keep tyres"
	Compound string // "soft"/"medium"/"hard"/"wet"/"intermediate", or "" if unspecified

	// Generic numeric for ActPressure/ActWing/ActGrille/ActBrakeDuct: an absolute
	// target (Value/ValueSet) or a relative click delta (Delta/DeltaSet).
	Value    float64
	ValueSet bool
	Delta    int
	DeltaSet bool

	// Brake-duct openness (ActBrakeDuct).
	Open   bool
	Closed bool

	// Boolean toggles: ActBrakes (replace brakes), ActDamage (repair). On=do it.
	On bool

	// Pit (ActPit).
	PitOn bool // true = request a stop, false = cancel the stop
}

// Important reports whether the action must be confirmed before it is applied.
// Every pit-strategy change is important; readouts (none yet) would not be.
func (a Action) Important() bool { return true }

// Utterance is the parsed result of one transcript. It is either a command (one
// or more Actions) or a bare confirmation response (Affirm / Cancel) to a pending
// command. Actions take precedence: a transcript that names a command is treated
// as a fresh command even if it also contains an affirmation word.
type Utterance struct {
	Raw     string
	Actions []Action
	Affirm  bool
	Cancel  bool
}

var (
	numRe      = regexp.MustCompile(`\b(\d{1,3})(?:\.\d+)?\b`)
	nonWordRe  = regexp.MustCompile(`[^a-z0-9 ]+`)
	multiSpace = regexp.MustCompile(`\s+`)
)

// numberWords maps spelled-out numbers whisper sometimes emits to their values.
var numberWords = map[string]int{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
	"sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20,
	"thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70,
	"eighty": 80, "ninety": 90, "hundred": 100,
}

var affirmWords = map[string]bool{
	"yes": true, "yeah": true, "yep": true, "yup": true, "confirm": true,
	"confirmed": true, "affirmative": true, "okay": true, "ok": true,
	"go": true, "do": true, "send": true, "correct": true,
}

var cancelWords = map[string]bool{
	"no": true, "cancel": true, "stop": true, "abort": true, "negative": true,
	"nevermind": true, "scratch": true, "discard": true,
}

// Parse turns a raw transcript into an Utterance. It is deterministic and
// side-effect-free. An empty/garbled transcript yields an Utterance with no
// actions and neither Affirm nor Cancel set (the engine treats that as "ignore").
func Parse(raw string) Utterance {
	u := Utterance{Raw: raw}
	norm := normalize(raw)
	if norm == "" {
		return u
	}
	words := strings.Fields(norm)

	u.Actions = parseActions(norm, words)
	if len(u.Actions) > 0 {
		return u
	}

	// No command recognized: treat the utterance as a confirm/cancel response.
	// "cancel pit"/"don't pit" are handled as actions above, so a bare cancel word
	// here is an answer to a pending confirmation.
	if anyWord(words, affirmWords) && !anyWord(words, cancelWords) {
		u.Affirm = true
		return u
	}
	if anyWord(words, cancelWords) {
		u.Cancel = true
	}
	return u
}

// parseActions extracts every recognized pit action from the normalized text.
func parseActions(norm string, words []string) []Action {
	var actions []Action

	if a, ok := parseEnergy(norm, words); ok {
		actions = append(actions, a)
	}
	if a, ok := parseFuel(norm, words); ok {
		actions = append(actions, a)
	}
	if a, ok := parsePressure(norm, words); ok {
		actions = append(actions, a)
	}
	if a, ok := parseBrakeDuct(norm, words); ok {
		actions = append(actions, a)
	}
	if a, ok := parseBrakes(norm); ok {
		actions = append(actions, a)
	}
	if a, ok := parseWing(norm, words); ok {
		actions = append(actions, a)
	}
	if a, ok := parseGrille(norm, words); ok {
		actions = append(actions, a)
	}
	if a, ok := parseDamage(norm); ok {
		actions = append(actions, a)
	}
	if a, ok := parseTyres(norm); ok {
		actions = append(actions, a)
	}
	if a, ok := parsePit(norm, words); ok {
		actions = append(actions, a)
	}
	return actions
}

// tyreSelFrom reads a front/rear/all selection from the text, defaulting to all.
func tyreSelFrom(norm string) TyreSel {
	switch {
	case strings.Contains(norm, "front"):
		return TyreFront
	case strings.Contains(norm, "rear") || strings.Contains(norm, "back"):
		return TyreRear
	default:
		return TyreAll
	}
}

// parseRelative reads a relative adjustment ("more"/"less", "up"/"down",
// "plus N"/"minus N"). The magnitude is the named number, else 1.
func parseRelative(norm string, words []string) (int, bool) {
	up := strings.Contains(norm, "more") || strings.Contains(norm, "increase") ||
		strings.Contains(norm, "plus") || strings.Contains(norm, "raise") ||
		strings.Contains(norm, "higher") || slices.Contains(words, "up")
	down := strings.Contains(norm, "less") || strings.Contains(norm, "decrease") ||
		strings.Contains(norm, "reduce") || strings.Contains(norm, "minus") ||
		strings.Contains(norm, "lower") || slices.Contains(words, "down")
	if !up && !down {
		return 0, false
	}
	mag := 1
	if n, ok := findNumber(norm, words); ok && n > 0 {
		mag = n
	}
	if down {
		return -mag, true
	}
	return mag, true
}

func parsePressure(norm string, words []string) (Action, bool) {
	if !strings.Contains(norm, "pressure") && !strings.Contains(norm, "psi") {
		return Action{}, false
	}
	a := Action{Type: ActPressure, Which: tyreSelFrom(norm)}
	if d, ok := parseRelative(norm, words); ok {
		a.Delta, a.DeltaSet = d, true
		return a, true
	}
	if n, ok := findNumber(norm, words); ok {
		a.Value, a.ValueSet = float64(n), true
		return a, true
	}
	return Action{}, false
}

func parseBrakeDuct(norm string, words []string) (Action, bool) {
	if !strings.Contains(norm, "duct") {
		return Action{}, false
	}
	a := Action{Type: ActBrakeDuct, Which: tyreSelFrom(norm)}
	switch {
	case strings.Contains(norm, "open"):
		a.Open = true
		return a, true
	case strings.Contains(norm, "close") || strings.Contains(norm, "closed") || strings.Contains(norm, "shut"):
		a.Closed = true
		return a, true
	}
	if d, ok := parseRelative(norm, words); ok {
		a.Delta, a.DeltaSet = d, true
		return a, true
	}
	if n, ok := findNumber(norm, words); ok {
		a.Value, a.ValueSet = float64(n), true
		return a, true
	}
	return Action{}, false
}

func parseBrakes(norm string) (Action, bool) {
	// "replace/change/new/fresh brakes" — but NOT "brake duct" (handled above).
	if !strings.Contains(norm, "brake") || strings.Contains(norm, "duct") {
		return Action{}, false
	}
	change := strings.Contains(norm, "replace") || strings.Contains(norm, "change") ||
		strings.Contains(norm, "new") || strings.Contains(norm, "fresh") || strings.Contains(norm, "swap")
	keep := strings.Contains(norm, "keep") || strings.Contains(norm, "dont") || strings.Contains(norm, "no ")
	if !change && !keep {
		return Action{}, false
	}
	a := Action{Type: ActBrakes, On: true}
	if keep { // negation wins ("don't replace brakes", "keep brakes")
		a.On = false
	}
	return a, true
}

func parseWing(norm string, words []string) (Action, bool) {
	if !strings.Contains(norm, "wing") {
		return Action{}, false
	}
	a := Action{Type: ActWing}
	if d, ok := parseRelative(norm, words); ok {
		a.Delta, a.DeltaSet = d, true
		return a, true
	}
	if n, ok := findNumber(norm, words); ok {
		a.Value, a.ValueSet = float64(n), true
		return a, true
	}
	return Action{}, false
}

func parseGrille(norm string, words []string) (Action, bool) {
	if !strings.Contains(norm, "grille") && !strings.Contains(norm, "grill") &&
		!strings.Contains(norm, "radiator") && !strings.Contains(norm, "tape") {
		return Action{}, false
	}
	a := Action{Type: ActGrille}
	if d, ok := parseRelative(norm, words); ok {
		a.Delta, a.DeltaSet = d, true
		return a, true
	}
	if n, ok := findNumber(norm, words); ok {
		a.Value, a.ValueSet = float64(n), true
		return a, true
	}
	return Action{}, false
}

func parseDamage(norm string) (Action, bool) {
	if !strings.Contains(norm, "damage") && !strings.Contains(norm, "repair") && !strings.Contains(norm, "fix") {
		return Action{}, false
	}
	a := Action{Type: ActDamage, On: true}
	if strings.Contains(norm, "dont") || strings.Contains(norm, "no repair") ||
		strings.Contains(norm, "no damage") || strings.Contains(norm, "skip") {
		a.On = false
	}
	return a, true
}

func parseEnergy(norm string, words []string) (Action, bool) {
	// "virtual energy", "energy to 80", "energy 50 percent", "full energy".
	if !strings.Contains(norm, "energy") {
		return Action{}, false
	}
	a := Action{Type: ActEnergy}
	switch {
	case strings.Contains(norm, "full") || strings.Contains(norm, "max"):
		a.FuelMax = true
		return a, true
	case strings.Contains(norm, "no energy") || strings.Contains(norm, "zero energy"):
		a.Liters = 0
		a.FuelSet = true
		return a, true
	}
	if n, ok := findNumber(norm, words); ok {
		a.Liters = float64(n)
		a.FuelSet = true
		return a, true
	}
	return Action{}, false
}

func parseFuel(norm string, words []string) (Action, bool) {
	if !strings.Contains(norm, "fuel") && !strings.Contains(norm, "fill") {
		return Action{}, false
	}
	a := Action{Type: ActFuel}
	switch {
	case strings.Contains(norm, "full") || strings.Contains(norm, "fill up") ||
		strings.Contains(norm, "fill it") || strings.Contains(norm, "max") ||
		strings.Contains(norm, "brim"):
		a.FuelMax = true
		return a, true
	case strings.Contains(norm, "no fuel") || strings.Contains(norm, "empty") ||
		strings.Contains(norm, "zero fuel") || strings.Contains(norm, "dont fuel") ||
		strings.Contains(norm, "no more fuel"):
		a.Liters = 0
		a.FuelSet = true
		return a, true
	}
	if n, ok := findNumber(norm, words); ok {
		a.Liters = float64(n)
		a.FuelSet = true
		return a, true
	}
	// "fuel" with no quantity is too ambiguous to act on safely.
	return Action{}, false
}

func parseTyres(norm string) (Action, bool) {
	// "tyre pressure" / "tyre ducts" are their own actions, not a tyre change.
	if strings.Contains(norm, "pressure") || strings.Contains(norm, "psi") || strings.Contains(norm, "duct") {
		return Action{}, false
	}
	hasTyreWord := strings.Contains(norm, "tyre") || strings.Contains(norm, "tire") ||
		strings.Contains(norm, "rubber")
	compound := parseCompound(norm)
	if !hasTyreWord && compound == "" {
		return Action{}, false
	}
	a := Action{Type: ActTyres, Compound: compound}

	if strings.Contains(norm, "no tyre") || strings.Contains(norm, "no tire") ||
		strings.Contains(norm, "dont change") || strings.Contains(norm, "keep tyre") ||
		strings.Contains(norm, "keep tire") || strings.Contains(norm, "no change") {
		a.NoTyres = true
		a.Which = TyreUnset
		return a, true
	}

	switch {
	case strings.Contains(norm, "all") || strings.Contains(norm, "four") ||
		strings.Contains(norm, " 4 ") || strings.HasSuffix(norm, " 4"):
		a.Which = TyreAll
	case strings.Contains(norm, "front"):
		a.Which = TyreFront
	case strings.Contains(norm, "rear") || strings.Contains(norm, "back"):
		a.Which = TyreRear
	case hasTyreWord:
		// "change tyres" with no qualifier means all four.
		a.Which = TyreAll
	default:
		// A bare compound change ("go soft") leaves Which unset; the executor only
		// touches the compound component.
		a.Which = TyreUnset
	}
	return a, true
}

func parsePit(norm string, words []string) (Action, bool) {
	cancel := strings.Contains(norm, "cancel pit") || strings.Contains(norm, "cancel the pit") ||
		strings.Contains(norm, "dont pit") || strings.Contains(norm, "no pit") ||
		strings.Contains(norm, "stay out") || strings.Contains(norm, "cancel box")
	request := slices.Contains(words, "box") || strings.Contains(norm, "pit this") ||
		strings.Contains(norm, "pit now") || strings.Contains(norm, "request pit") ||
		strings.Contains(norm, "pit stop") || strings.Contains(norm, "box this") ||
		strings.Contains(norm, "pit lap") || strings.Contains(norm, "come in")
	switch {
	case cancel:
		return Action{Type: ActPit, PitOn: false}, true
	case request:
		return Action{Type: ActPit, PitOn: true}, true
	}
	return Action{}, false
}

// parseCompound returns the tyre compound named in the text, if any.
func parseCompound(norm string) string {
	switch {
	case strings.Contains(norm, "soft"):
		return "soft"
	case strings.Contains(norm, "medium"):
		return "medium"
	case strings.Contains(norm, "hard"):
		return "hard"
	case strings.Contains(norm, "wet") || strings.Contains(norm, "rain"):
		return "wet"
	case strings.Contains(norm, "intermediate") || strings.Contains(norm, "inter"):
		return "intermediate"
	default:
		return ""
	}
}

// findNumber returns the first number in the text, parsed from digits or a single
// spelled-out number word (handles "thirty" and "thirty five").
func findNumber(norm string, words []string) (int, bool) {
	if m := numRe.FindString(norm); m != "" {
		if n, err := strconv.Atoi(m); err == nil {
			return n, true
		}
	}
	// Spelled-out: a tens word optionally followed by a units word.
	for i, w := range words {
		base, ok := numberWords[w]
		if !ok {
			continue
		}
		if base >= 20 && base%10 == 0 && i+1 < len(words) {
			if add, ok := numberWords[words[i+1]]; ok && add < 10 {
				return base + add, true
			}
		}
		return base, true
	}
	return 0, false
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Drop apostrophes so contractions collapse ("don't" -> "dont") instead of
	// splitting into two tokens; then turn any other punctuation into spaces.
	s = strings.NewReplacer("'", "", "’", "").Replace(s)
	s = nonWordRe.ReplaceAllString(s, " ")
	s = multiSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func anyWord(words []string, set map[string]bool) bool {
	for _, w := range words {
		if set[w] {
			return true
		}
	}
	return false
}
