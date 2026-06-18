package voice

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"telemetry-handler/game/lmu/rest"
)

// pressureOpts builds a pressure option list "136".."160 (+24)".
func pressureOpts() []string {
	out := make([]string, 0, 25)
	for v := 136; v <= 160; v++ {
		if v == 136 {
			out = append(out, "136")
		} else {
			out = append(out, fmt.Sprintf("%d (+%d)", v, v-136))
		}
	}
	return out
}

// fakeController is a stand-in for *rest.Client: it serves a fixed pit menu and
// records the writes Resolve/Apply would send to the game.
type fakeController struct {
	menu    []rest.PitMenuItem
	writes  [][2]int // {pmc, setting}
	menuErr error
	setErr  error
}

func (f *fakeController) PitMenu(context.Context) ([]rest.PitMenuItem, error) {
	return f.menu, f.menuErr
}

func (f *fakeController) SetPitMenuValue(_ context.Context, pmc, setting int) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.writes = append(f.writes, [2]int{pmc, setting})
	return nil
}

// sampleMenu mirrors a real LMU hypercar pit menu (captured live): VIRTUAL ENERGY
// instead of litre fuel, an aggregate TIRES component plus per-corner ones whose
// options ARE the compound choices.
func sampleMenu() []rest.PitMenuItem {
	tyreOpts := []string{"No Change", "New Medium ", "New Wet ", "Used Medium  0%"}
	press := pressureOpts()
	return []rest.PitMenuItem{
		{Name: "DAMAGE:", PMCValue: 2, Settings: []string{"N/A"}},
		{Name: "VIRTUAL ENERGY:", PMCValue: 6, Settings: []string{"0% 0 laps", "25% 5 laps", "50% 10 laps", "75% 15 laps", "100% 21 laps"}},
		{Name: "FUEL RATIO:", PMCValue: 7, Settings: []string{"0.50", "0.91", "1.00"}},
		{Name: "TIRES:", PMCValue: 8, Settings: append(append([]string{}, tyreOpts...), "Mixed Tyres")},
		{Name: "FL TIRE:", PMCValue: 13, Settings: tyreOpts},
		{Name: "FR TIRE:", PMCValue: 14, Settings: tyreOpts},
		{Name: "RL TIRE:", PMCValue: 15, Settings: tyreOpts},
		{Name: "RR TIRE:", PMCValue: 16, Settings: tyreOpts},
		{Name: "R WING:", PMCValue: 20, CurrentSetting: 3, Settings: []string{"6.3 deg (-3)", "7.3 deg (-2)", "8.3 deg (-1)", "9.3 deg", "10.3 deg (+1)", "11.3 deg (+2)", "12.3 deg (+3)"}},
		{Name: "GRILLE:", PMCValue: 22, Settings: []string{"1", "2 (+1)", "3 (+2)", "4 (+3)"}},
		{Name: "FL PRESS:", PMCValue: 25, Settings: press},
		{Name: "FR PRESS:", PMCValue: 26, Settings: press},
		{Name: "RL PRESS:", PMCValue: 27, Settings: press},
		{Name: "RR PRESS:", PMCValue: 28, Settings: press},
		{Name: "F BRAKE DUCT:", PMCValue: 33, Settings: []string{"Open", "20%", "40%", "60%", "80%", "Closed"}},
		{Name: "R BRAKE DUCT:", PMCValue: 34, Settings: []string{"Open", "25%", "50%", "75%", "Closed"}},
		{Name: "REPLACE BRAKES:", PMCValue: 35, Settings: []string{"No", "Yes"}},
	}
}

// litreFuelMenu is a GT3/LMP2-style menu that DOES have a litre FUEL component.
func litreFuelMenu() []rest.PitMenuItem {
	return []rest.PitMenuItem{
		{Name: "FUEL:", PMCValue: 1, Settings: []string{"No Change", "+10", "+20", "+30", "+40", "+50"}},
		{Name: "FUEL RATIO:", PMCValue: 7, Settings: []string{"0.50", "1.00"}},
	}
}

func resolve(t *testing.T, c Controller, text string) Plan {
	t.Helper()
	u := Parse(text)
	if len(u.Actions) == 0 {
		t.Fatalf("%q parsed to no actions", text)
	}
	plan, err := Resolve(context.Background(), c, u.Actions)
	if err != nil {
		t.Fatalf("%q: resolve: %v", text, err)
	}
	return plan
}

func TestResolveEnergy(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	// "50%" is index 2 on VIRTUAL ENERGY (pmc 6).
	if p := resolve(t, c, "energy to 50"); p.Writes[0] != (PitWrite{PMC: 6, Setting: 2, Name: "VIRTUAL ENERGY:", Label: "50% 10 laps"}) {
		t.Fatalf("energy 50: got %+v", p.Writes)
	}
	// Nearest: 60% -> 50% (index 2) is closer than 75% (index 3)? 60 is 10 from 50
	// and 15 from 75, so index 2.
	if p := resolve(t, c, "energy 60 percent"); p.Writes[0].Setting != 2 {
		t.Errorf("energy 60: want setting 2, got %d", p.Writes[0].Setting)
	}
	// Max -> 100% (index 4).
	if p := resolve(t, c, "full energy"); p.Writes[0].Setting != 4 {
		t.Errorf("full energy: want setting 4, got %d", p.Writes[0].Setting)
	}
}

func TestResolveFuelLitre(t *testing.T) {
	c := &fakeController{menu: litreFuelMenu()}
	// "+30" is index 3; must NOT match "FUEL RATIO".
	if p := resolve(t, c, "fuel to 30"); p.Writes[0].PMC != 1 || p.Writes[0].Setting != 3 {
		t.Fatalf("fuel 30: got %+v", p.Writes)
	}
	// On a hypercar (no litre fuel), a fuel command maps to nothing.
	hyper := &fakeController{menu: sampleMenu()}
	if p := resolve(t, hyper, "fuel to 30"); len(p.Writes) != 0 {
		t.Errorf("fuel on hypercar should map to nothing, got %+v", p.Writes)
	}
}

// cornerSettings collapses a tyre plan to {pmc: setting} for the four corners.
func cornerSettings(p Plan) map[int]int {
	m := map[int]int{}
	for _, w := range p.Writes {
		m[w.PMC] = w.Setting
	}
	return m
}

func TestResolveTyresAll(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	// "change all tyres" writes the FOUR corners (13/14/15/16), NOT the aggregate
	// TIRES (8) — writing the aggregate alone leaves the corners unchanged and the
	// game reads "Mixed". First New option (New Medium, index 1).
	p := resolve(t, c, "change all tyres")
	if len(p.Writes) != 4 {
		t.Fatalf("all tyres: expected 4 corner writes, got %+v", p.Writes)
	}
	got := cornerSettings(p)
	for _, pmc := range []int{13, 14, 15, 16} {
		if got[pmc] != 1 {
			t.Errorf("all tyres: pmc %d want setting 1, got %d", pmc, got[pmc])
		}
	}
	// "all tyres wet" -> New Wet (index 2) on all four corners.
	if g := cornerSettings(resolve(t, c, "all tyres wet")); g[13] != 2 || g[16] != 2 {
		t.Errorf("all wet: got %+v", g)
	}
	// Bare compound ("new medium") -> all four corners, New Medium.
	if g := cornerSettings(resolve(t, c, "new medium")); g[13] != 1 || g[16] != 1 {
		t.Errorf("new medium: got %+v", g)
	}
	// "dont change tyres" -> No Change (index 0) on all four.
	if g := cornerSettings(resolve(t, c, "dont change tyres")); g[13] != 0 || g[16] != 0 {
		t.Errorf("no tyres: got %+v", g)
	}
}

func TestResolveTyresAggregateFallback(t *testing.T) {
	// A car exposing only the aggregate TIRES component (no per-corner) falls back
	// to writing it.
	c := &fakeController{menu: []rest.PitMenuItem{
		{Name: "TIRES:", PMCValue: 8, Settings: []string{"No Change", "New Medium ", "New Wet "}},
	}}
	p := resolve(t, c, "change all tyres")
	if len(p.Writes) != 1 || p.Writes[0].PMC != 8 || p.Writes[0].Setting != 1 {
		t.Errorf("aggregate fallback: got %+v", p.Writes)
	}
}

func TestResolveTyresPerCorner(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	// "front tyres" writes BOTH FL (13) and FR (14).
	p := resolve(t, c, "front tyres")
	if len(p.Writes) != 2 {
		t.Fatalf("front: expected 2 writes, got %+v", p.Writes)
	}
	got := map[int]int{p.Writes[0].PMC: p.Writes[0].Setting, p.Writes[1].PMC: p.Writes[1].Setting}
	if got[13] != 1 || got[14] != 1 {
		t.Errorf("front: got %+v", got)
	}
	// "rear tyres wet" writes RL (15) and RR (16) -> New Wet (index 2).
	pr := resolve(t, c, "rear tyres wet")
	if len(pr.Writes) != 2 {
		t.Fatalf("rear: expected 2 writes, got %+v", pr.Writes)
	}
	gr := map[int]int{pr.Writes[0].PMC: pr.Writes[0].Setting, pr.Writes[1].PMC: pr.Writes[1].Setting}
	if gr[15] != 2 || gr[16] != 2 {
		t.Errorf("rear wet: got %+v", gr)
	}
}

func TestResolveTyresUnknownCompoundFallsBackToNew(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	// "soft" isn't offered on this car; fall back to the first New option.
	if p := resolve(t, c, "all tyres soft"); p.Writes[0].Setting != 1 {
		t.Errorf("soft fallback: want New Medium (1), got %d", p.Writes[0].Setting)
	}
}

func TestResolvePitMissing(t *testing.T) {
	// LMU's pit menu has no pit-request component; "box this lap" maps to nothing.
	c := &fakeController{menu: sampleMenu()}
	plan := resolve(t, c, "box this lap")
	if len(plan.Writes) != 0 {
		t.Fatalf("box: expected no writes, got %+v", plan.Writes)
	}
	if plan.Important() {
		t.Error("a plan with no writes should not be important")
	}
}

func TestResolvePressure(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	// "front pressure 140" -> FL(25)+FR(26) at index 4 ("140 (+4)").
	p := resolve(t, c, "front pressure 140")
	if len(p.Writes) != 2 {
		t.Fatalf("front pressure: expected 2 writes, got %+v", p.Writes)
	}
	g := cornerSettings(p)
	if g[25] != 4 || g[26] != 4 {
		t.Errorf("front pressure 140: got %+v", g)
	}
	// "all pressures 145" -> four corners at index 9.
	if len(resolve(t, c, "all pressures 145").Writes) != 4 {
		t.Errorf("all pressures: expected 4 writes")
	}
	if cornerSettings(resolve(t, c, "rear pressure 150"))[27] != 14 {
		t.Errorf("rear pressure 150: RL want index 14")
	}
}

func TestResolveBrakeDuct(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	// "open front ducts" -> F BRAKE DUCT (33) index 0 (Open).
	if p := resolve(t, c, "open front ducts"); p.Writes[0].PMC != 33 || p.Writes[0].Setting != 0 {
		t.Errorf("open front duct: got %+v", p.Writes)
	}
	// "close rear duct" -> R BRAKE DUCT (34) last index (Closed = 4).
	if p := resolve(t, c, "close rear duct"); p.Writes[0].PMC != 34 || p.Writes[0].Setting != 4 {
		t.Errorf("close rear duct: got %+v", p.Writes)
	}
	// "front brake duct 40 percent" -> 40% (index 2).
	if p := resolve(t, c, "front brake duct 40 percent"); p.Writes[0].Setting != 2 {
		t.Errorf("front duct 40: got %+v", p.Writes)
	}
}

func TestResolveReplaceBrakes(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	if p := resolve(t, c, "replace brakes"); p.Writes[0].PMC != 35 || p.Writes[0].Setting != 1 {
		t.Errorf("replace brakes: got %+v", p.Writes)
	}
	if p := resolve(t, c, "keep brakes"); p.Writes[0].PMC != 35 || p.Writes[0].Setting != 0 {
		t.Errorf("keep brakes: got %+v", p.Writes)
	}
}

func TestResolveWing(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	// Relative: "more wing" -> current 3 + 1 = index 4.
	if p := resolve(t, c, "more wing"); p.Writes[0].PMC != 20 || p.Writes[0].Setting != 4 {
		t.Errorf("more wing: got %+v", p.Writes)
	}
	// "less wing" -> 3 - 1 = index 2.
	if p := resolve(t, c, "less wing"); p.Writes[0].Setting != 2 {
		t.Errorf("less wing: got %+v", p.Writes)
	}
	// "wing plus 2" -> 3 + 2 = index 5.
	if p := resolve(t, c, "wing plus 2"); p.Writes[0].Setting != 5 {
		t.Errorf("wing plus 2: got %+v", p.Writes)
	}
	// Absolute: "wing to 11" -> closest leading degree 11.3 = index 5.
	if p := resolve(t, c, "wing to 11"); p.Writes[0].Setting != 5 {
		t.Errorf("wing to 11: got %+v", p.Writes)
	}
}

func TestResolveGrille(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	if p := resolve(t, c, "grille 3"); p.Writes[0].PMC != 22 || p.Writes[0].Setting != 2 {
		t.Errorf("grille 3: got %+v", p.Writes)
	}
}

func TestResolveDamageNoneToRepair(t *testing.T) {
	// The captured menu only offers "N/A" -> repair maps to nothing.
	c := &fakeController{menu: sampleMenu()}
	if p := resolve(t, c, "repair damage"); len(p.Writes) != 0 {
		t.Errorf("repair with only N/A: expected no writes, got %+v", p.Writes)
	}
	// A damaged car with real options repairs to the "All" option.
	dmg := &fakeController{menu: []rest.PitMenuItem{
		{Name: "DAMAGE:", PMCValue: 2, Settings: []string{"None", "Bodywork", "All"}},
	}}
	if p := resolve(t, dmg, "repair damage"); p.Writes[0].PMC != 2 || p.Writes[0].Setting != 2 {
		t.Errorf("repair all: got %+v", p.Writes)
	}
}

func TestResolveTyrePressureNotDoubleParsed(t *testing.T) {
	// "front tyre pressure 140" must be a pressure action only, not also a tyre
	// change.
	u := Parse("front tyre pressure 140")
	if len(u.Actions) != 1 || u.Actions[0].Type != ActPressure {
		t.Fatalf("expected single pressure action, got %+v", u.Actions)
	}
}

func TestPlanApply(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	plan := resolve(t, c, "front tyres")
	if err := plan.Apply(context.Background(), c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(c.writes) != 2 {
		t.Fatalf("expected 2 game writes, got %v", c.writes)
	}
}

func TestPlanApplyError(t *testing.T) {
	c := &fakeController{menu: sampleMenu(), setErr: errors.New("game closed")}
	plan := resolve(t, c, "energy to 50")
	if err := plan.Apply(context.Background(), c); err == nil {
		t.Fatal("expected apply error")
	}
}
