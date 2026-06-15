package minds

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
)

func TestBriefIsDeterministicAndReflectsStructure(t *testing.T) {
	p := DefaultPersonality("Dani")
	p.Traits["humour"] = 0.9
	p.Values["caution"] = 0.1
	p.Communication.Formality = 0.2
	b1, b2 := p.Brief(), p.Brief()
	if b1 != b2 {
		t.Fatal("brief not deterministic")
	}
	for _, want := range []string{"You are Dani", "Pronounced traits: humour", "little weight on: caution", "casually", "I do not know"} {
		if !strings.Contains(b1, want) {
			t.Errorf("brief missing %q:\n%s", want, b1)
		}
	}
}

func TestDiffAndOverridesRoundTrip(t *testing.T) {
	base := DefaultPersonality("t")
	mod := base
	mod.Traits = map[string]float64{}
	for k, v := range base.Traits {
		mod.Traits[k] = v
	}
	mod.Traits["humour"] = 0.9
	mod.Identity.Role = "investigator"
	mod.Behaviour.NotifyProactively = true
	d := Diff(base, mod)
	if len(d) != 3 {
		t.Fatalf("expected 3 changes, got %+v", d)
	}
	ov := Overrides(base, mod)
	back := ApplyOverrides(base, ov)
	if len(Diff(mod, back)) != 0 {
		t.Fatalf("overrides did not round-trip: %+v", Diff(mod, back))
	}
	// Applying the same overrides to a changed template keeps the overrides and takes the rest from the template.
	base2 := base
	base2.Values = map[string]float64{}
	for k, v := range base.Values {
		base2.Values[k] = v
	}
	base2.Values["kindness"] = 0.95
	res := ApplyOverrides(base2, ov)
	if res.Values["kindness"] != 0.95 || res.Traits["humour"] != 0.9 || res.Identity.Role != "investigator" {
		t.Fatalf("template change + overrides wrong: %+v", res)
	}
}

func TestNormaliseClampsAndFills(t *testing.T) {
	p := Personality{Identity: Identity{Name: "x"}, Values: map[string]float64{"privacy": 7, "bogus": 1}, Traits: nil}
	p.Normalise()
	if p.Values["privacy"] != 1 || len(p.Values) != len(ValueKeys) || len(p.Traits) != len(TraitKeys) || p.Communication.Language != "en" {
		t.Fatalf("normalise wrong: %+v", p)
	}
	if _, ok := p.Values["bogus"]; ok {
		t.Fatal("unknown key kept")
	}
}

func TestStoreLifecycle(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	s := NewStore(pool)

	if _, err := s.Create(ctx, CreateParams{Slug: "Bad Slug!"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid slug, got %v", err)
	}
	m, err := s.Create(ctx, CreateParams{Slug: "dani", DisplayName: "Dani", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, CreateParams{Slug: "DANI"}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	got, err := s.Resolve(ctx, "dani")
	if err != nil || got.ID != m.ID || got.PersonalityVersion != 1 || got.Config.WorkspaceCapacity != 7 {
		t.Fatalf("resolve: %v %+v", err, got)
	}
	sm, err := s.GetSelfModel(ctx, m.ID)
	if err != nil || len(sm.Limitations) < 3 || sm.Name != "Dani" {
		t.Fatalf("self model: %v %+v", err, sm)
	}

	// New personality version, then restore.
	p := got.Personality
	p.Traits["curiosity"] = 0.95
	p.FreeText = "Loves old hardware."
	v2, err := s.SavePersonality(ctx, m.ID, p, "more curious", "test")
	if err != nil || v2.Version != 2 {
		t.Fatalf("save: %v %+v", err, v2)
	}
	v3, err := s.Restore(ctx, m.ID, 1, "test")
	if err != nil || v3.Version != 3 || v3.Personality.Traits["curiosity"] != 0.5 {
		t.Fatalf("restore: %v %+v", err, v3)
	}
	vs, _ := s.ListVersions(ctx, m.ID)
	if len(vs) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(vs))
	}
	// History is immutable.
	if _, err := pool.Exec(ctx, `DELETE FROM personality_versions WHERE mind_id=$1`, m.ID); err == nil {
		t.Fatal("personality history should be append-only")
	}

	// Templates and overrides survive re-application.
	tp := DefaultPersonality("Investigator")
	tp.Traits["scepticism"] = 0.9
	tmpl, err := s.CreateTemplate(ctx, "investigator", "sceptical analyst", tp)
	if err != nil {
		t.Fatal(err)
	}
	v4, err := s.ApplyTemplate(ctx, m.ID, &tmpl.ID, "test")
	if err != nil || v4.Personality.Traits["scepticism"] != 0.9 || v4.Personality.Identity.DisplayName != "Dani" {
		t.Fatalf("apply template: %v %+v", err, v4.Personality)
	}
	p4 := v4.Personality
	p4.Traits["humour"] = 0.85
	v5, err := s.SavePersonality(ctx, m.ID, p4, "funnier", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v5.Overrides["traits"].(map[string]any)["humour"]; !ok {
		t.Fatalf("override not recorded: %+v", v5.Overrides)
	}
	tp.Values["kindness"] = 0.99
	if _, err := s.UpdateTemplate(ctx, tmpl.ID, "investigator", "v2", tp); err != nil {
		t.Fatal(err)
	}
	v6, err := s.ApplyTemplate(ctx, m.ID, &tmpl.ID, "test")
	if err != nil || v6.Personality.Values["kindness"] != 0.99 || v6.Personality.Traits["humour"] != 0.85 {
		t.Fatalf("re-apply lost overrides or template change: %+v", v6.Personality)
	}
	if err := s.DeleteTemplate(ctx, tmpl.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("in-use template deletion should be refused, got %v", err)
	}

	// Clone copies personality only.
	c, err := s.Clone(ctx, m.ID, "dani-2", "Dani Two", "test")
	if err != nil || c.Personality.Traits["humour"] != 0.85 || c.ClonedFromMind == nil || *c.ClonedFromMind != m.ID || c.TemplateID == nil {
		t.Fatalf("clone: %v %+v", err, c)
	}
	// Status transitions and list filtering.
	st := StatusArchived
	if _, err := s.Update(ctx, c.ID, UpdateParams{Status: &st}); err != nil {
		t.Fatal(err)
	}
	live, _ := s.List(ctx, false)
	all, _ := s.List(ctx, true)
	if len(live) != 1 || len(all) != 2 {
		t.Fatalf("list filtering wrong: %d live %d all", len(live), len(all))
	}
}
