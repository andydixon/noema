package replay

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/identity"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay/replaytest"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func TestReplayIsExactWithoutModel(t *testing.T) {
	h := replaytest.NewHarness(t)
	ctx := context.Background()
	src, err := h.Minds.Create(ctx, minds.CreateParams{Slug: "src", DisplayName: "Source"})
	if err != nil {
		t.Fatal(err)
	}
	// Live run on the source, driven synchronously: one stimulus, settle.
	live, err := NewDriver(ctx, h.Brain, src, "test")
	if err != nil {
		t.Fatal(err)
	}
	sn, err := h.Snaps.Create(ctx, src, "t0", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.Observe(ctx, "operator", "Andy said the payments service failed after the migration", 0.8, nil); err != nil {
		t.Fatal(err)
	}
	produced, cycles, err := live.Settle(ctx)
	if err != nil || cycles == 0 || len(Digest(produced)) == 0 {
		t.Fatalf("live run produced nothing: %v cycles=%d", err, cycles)
	}
	last, _ := h.Events.Query(ctx, evstore.Filter{MindID: src.ID, Limit: 1})
	svc := &Service{DB: h.Pool, Events: h.Events, Minds: h.Minds, Snapshots: h.Snaps, Brain: h.Brain, Identity: h.Identity, Log: slog.New(slog.DiscardHandler)}
	r, err := svc.Run(ctx, src, sn.ID, sn.EventSeq, last[0].Seq, "test")
	if err != nil {
		t.Fatalf("replay: %v %+v", err, r)
	}
	if r.Status != "done" || r.Summary.Steps != 1 || r.Summary.Exact != 1 || r.Summary.LLMLive != 0 {
		t.Fatalf("expected one exact step: %+v steps=%s", r.Summary, string(r.Steps))
	}
	// Reproducibility record: the runtime is recorded and compared with the
	// snapshot's; same process, same schema means an exact candidate with no
	// first divergence.
	if r.Identity.SchemaVersion == 0 || r.Identity.Build.GoVersion == "" || len(r.Identity.Deterministic) == 0 {
		t.Fatalf("identity not recorded: %+v", r.Identity)
	}
	if r.Compatibility != identity.CompatExactCandidate || r.Divergence != nil {
		t.Fatalf("compatibility %q diff %v divergence %+v", r.Compatibility, r.IdentityDiff, r.Divergence)
	}
	// A snapshot taken under another schema cannot be replayed faithfully.
	old, _ := h.Snaps.Get(ctx, sn.ID)
	old.Identity.SchemaVersion = 1
	oj, _ := json.Marshal(old.Identity)
	h.Pool.Exec(ctx, `UPDATE snapshots SET identity=$2 WHERE id=$1`, sn.ID, oj) //nolint:errcheck
	inc, err := svc.Run(ctx, src, sn.ID, sn.EventSeq, last[0].Seq, "test")
	if inc.Status != "incompatible" || inc.Compatibility != identity.CompatIncompatible || inc.Summary.Steps != 0 {
		t.Fatalf("incompatible replay should stop before running: %+v %v", inc, err)
	}
	h.Pool.Exec(ctx, `UPDATE snapshots SET identity=$2 WHERE id=$1`, sn.ID, mustJSON(r.Identity)) //nolint:errcheck
	steps := StepsOf(r)
	if len(steps) != 1 || !Same(steps[0].Original, steps[0].Replayed) || len(steps[0].Replayed) == 0 {
		t.Fatalf("step mismatch: %+v", steps)
	}
	sandbox, err := h.Minds.Get(ctx, *r.ReplayMindID)
	if err != nil || sandbox.Status != minds.StatusDisabled || sandbox.ForkedFromSnapshot == nil {
		t.Fatalf("sandbox mind wrong: %+v %v", sandbox, err)
	}
	// Provenance is reconstructed: beliefs in the sandbox cite the same
	// families in the same derivations as the source's.
	if a, b := provenanceDigest(t, h, src.ID), provenanceDigest(t, h, sandbox.ID); a != b {
		t.Fatalf("provenance differs after replay:\nsource:  %s\nsandbox: %s", a, b)
	}
	// The source's history is untouched by the replay.
	after, _ := h.Events.Query(ctx, evstore.Filter{MindID: src.ID, Limit: 1})
	if after[0].Seq != last[0].Seq {
		t.Fatal("replay wrote into the source mind")
	}
	// A redacted stimulus is not replayed and is labelled as such.
	if _, err := h.Pool.Exec(ctx, `INSERT INTO redactions (id, mind_id, target_kind, target_id, reason, content_hash, requested_by) VALUES ($1,$2,'event',$3,'test','sha256:0','test')`, ids.New(), src.ID, steps[0].StimulusID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.Pool.QueryRow(ctx, `SELECT noema_redact('event', $1, $2)`, steps[0].StimulusID, ids.New()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("redact: %v %d", err, n)
	}
	red, err := svc.Run(ctx, src, sn.ID, sn.EventSeq, last[0].Seq, "test")
	if err != nil || red.Summary.Redacted != 1 || red.Summary.Exact != 0 || StepsOf(red)[0].Label != "redacted" {
		t.Fatalf("redacted stimulus should be skipped and labelled: %v %+v", err, red.Summary)
	}
	// Digest normalisation ignores ids and numbers.
	if Normalise("selected 3 thoughts for 01a0a148-9034-7685-9794-cf5ec0c25fb7") != "selected <n> thoughts for <id>" {
		t.Fatal(Normalise("selected 3 thoughts for 01a0a148-9034-7685-9794-cf5ec0c25fb7"))
	}
	_ = events.Observation
}

// provenanceDigest summarises a mind's beliefs as proposition → sorted
// (derivation, family) pairs, ignoring ids that differ between minds.
func provenanceDigest(t *testing.T, h *replaytest.Harness, mind ids.ID) string {
	t.Helper()
	ctx := context.Background()
	bs := h.Brain.Services.Beliefs
	list, err := bs.List(ctx, mind, beliefs.Filter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, b := range list {
		rows, _ := bs.EvidenceFor(ctx, nil, mind, b.ID)
		var ev []string
		for _, r := range rows {
			ev = append(ev, r.Derivation+"/"+r.Family+"/"+r.ProvenanceStatus)
		}
		sort.Strings(ev)
		lines = append(lines, strings.ToLower(b.Proposition)+" ← "+strings.Join(ev, ","))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
