package privacy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/audit"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/epistemics"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// TestRedactionIsIrrecoverableButReferentiallyIntact: content goes, the
// row, its links, its type and time stay; the hash attests what was removed;
// derived rows are cascaded; stored archives are scrubbed; and the general
// append-only guarantee still holds for every other write.
func TestRedactionIsIrrecoverableButReferentiallyIntact(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ms := minds.NewStore(pool)
	m, _ := ms.Create(ctx, minds.CreateParams{Slug: "priv"})
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	mem := memory.NewStore(ctx, pool, nil)
	bs := beliefs.NewStore(pool)
	as := epistemics.NewStore(pool)
	snaps := &snapshots.Store{DB: pool, Events: es, Minds: ms}
	secret := "Andy said Steve steals laptops from the Leeds office"

	obs, err := es.Append(ctx, events.New{MindID: m.ID, Type: events.Observation, Source: "conversation", Summary: secret, Payload: events.ObservationPayload{Text: secret, Actor: "andy", Trust: events.TrustUser}, Metadata: map[string]any{"conversation_id": "c1", "channel": "conversation"}})
	if err != nil {
		t.Fatal(err)
	}
	child, _ := es.Append(ctx, events.New{MindID: m.ID, Type: events.Perception, Source: "brain.perceive", Summary: "perceived: " + secret, CausationID: obs[0].ID, ParentIDs: []ids.ID{obs[0].ID}})
	episode, _ := mem.Create(ctx, pool, memory.New{MindID: m.ID, Kind: concepts.Episodic, Epistemic: concepts.Observation, Content: "andy said: " + secret, Source: "conversation", Trust: events.TrustUser, Confidence: 1, Importance: 0.5, OccurredAt: time.Now(), OriginEvent: obs[0].ID})
	emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		out, err := es.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		return out[0], nil
	}
	res, _ := bs.Create(ctx, pool, emit, beliefs.New{MindID: m.ID, Proposition: "Steve steals laptops", Subject: "steve", Epistemic: concepts.Claim, Source: "conversation",
		Evidence: []beliefs.EvidenceInput{{Direction: "for", Kind: "assertion", Source: "asserted-by:andy", Family: "actor:andy", Derivation: beliefs.DerivationAssertion, Weight: 0.5, Credibility: 0.85, Note: "andy asserted it", RootIDs: []string{string(obs[0].ID)}}}})
	assertion, _ := as.Record(ctx, pool, epistemics.New{MindID: m.ID, Actor: "andy", Interpretation: epistemics.Interpret("andy", "Steve steals laptops"), SourceEvent: obs[0].ID, Context: secret, BeliefID: res.Belief.ID})
	convID, msgID, replyID, callID := ids.New(), ids.New(), ids.New(), ids.New()
	pool.Exec(ctx, `INSERT INTO conversations (id, mind_id, title, channel) VALUES ($1,$2,'t','conversation')`, convID, m.ID)                                                                                                               //nolint:errcheck
	pool.Exec(ctx, `INSERT INTO llm_calls (id, mind_id, role, provider, model, request_hash, request, response) VALUES ($1,$2,'respond','p','m','h', $3, $4)`, callID, m.ID, `{"messages":[{"content":"`+secret+`"}]}`, `{"text":"noted"}`) //nolint:errcheck
	if _, err := pool.Exec(ctx, `INSERT INTO messages (id, conversation_id, mind_id, role, actor, content, event_id) VALUES ($1,$2,$3,'user','andy',$4,$5)`, msgID, convID, m.ID, secret, obs[0].ID); err != nil {
		t.Fatal(err)
	}
	pool.Exec(ctx, `INSERT INTO messages (id, conversation_id, mind_id, role, actor, content, reply_to, llm_call_id) VALUES ($1,$2,$3,'mind','mind','Noted: Steve steals laptops',$4,$5)`, replyID, convID, m.ID, msgID, callID) //nolint:errcheck
	sn, err := snaps.Create(ctx, m, "before", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(mustArchive(t, snaps, sn.ID)); !strings.Contains(string(raw), "Leeds") {
		t.Fatal("test setup: archive should contain the secret before redaction")
	}

	svc := &Service{DB: pool, Events: es, Audit: audit.New(pool, log), Snapshots: snaps, Log: log}
	// Refuses bad requests and rows from other minds.
	other, _ := ms.Create(ctx, minds.CreateParams{Slug: "other"})
	if _, _, err := svc.Redact(ctx, Request{MindID: other.ID, Kind: KindEvent, ID: obs[0].ID, Reason: "x", By: "admin"}); err == nil {
		t.Fatal("redacting another mind's row must fail")
	}
	if _, _, err := svc.Redact(ctx, Request{MindID: m.ID, Kind: KindEvent, ID: obs[0].ID, By: "admin"}); err == nil {
		t.Fatal("a reason is required")
	}
	primary, cascaded, err := svc.Redact(ctx, Request{MindID: m.ID, Kind: KindEvent, ID: obs[0].ID, Reason: "data subject request", Policy: "GDPR art. 17", Cascade: true, By: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	// The event exists, keeps type, time, links and ids; content is a marker.
	ev, _ := es.Get(ctx, obs[0].ID)
	if ev.Summary != Marker || string(ev.Payload) != `{"redacted": true}` || ev.RedactedAt == nil || ev.Type != events.Observation || ev.Seq != obs[0].Seq {
		t.Fatalf("event after redaction: %+v payload=%s", ev, ev.Payload)
	}
	if ev.Metadata["conversation_id"] != "c1" || ev.Metadata["redacted"] != true {
		t.Fatalf("metadata should keep only structural keys: %+v", ev.Metadata)
	}
	if kids, _ := es.Children(ctx, obs[0].ID, 10); len(kids) != 1 || kids[0].ID != child[0].ID {
		t.Fatal("links to dependants must survive")
	}
	// The hash attests the removed content.
	want := sha256.Sum256(mustJSONB(t, pool, `SELECT jsonb_build_object('summary', $1::text, 'payload', $2::jsonb, 'metadata', $3::jsonb)`, secret, string(obs[0].Payload), mustJSON(obs[0].Metadata)))
	if primary.ContentHash != "sha256:"+hex.EncodeToString(want[:]) {
		t.Fatalf("hash: %s", primary.ContentHash)
	}
	// Cascade reached the episode, the message, the reply, the model call, the assertion and the belief.
	kinds := map[Kind]int{}
	for _, c := range cascaded {
		kinds[c.Kind]++
		if c.CascadeOf != primary.ID {
			t.Fatalf("cascade lineage missing: %+v", c)
		}
	}
	if kinds[KindMemory] != 1 || kinds[KindMessage] != 2 || kinds[KindLLMCall] != 1 || kinds[KindAssertion] != 1 || kinds[KindBelief] != 1 {
		t.Fatalf("cascade: %+v", kinds)
	}
	if mm, _ := mem.Get(ctx, m.ID, episode.ID); mm.Content != Marker || mm.RedactedAt == nil || mm.OriginEvent != obs[0].ID {
		t.Fatalf("memory: %+v", mm)
	}
	if b, _ := bs.Get(ctx, pool, m.ID, res.Belief.ID); b.Proposition != Marker || b.Confidence != res.Belief.Confidence {
		t.Fatalf("belief proposition should be redacted and its score untouched: %+v", b)
	}
	var vprop, note, msg, reply, req string
	var resp *string
	pool.QueryRow(ctx, `SELECT proposition FROM belief_versions WHERE belief_id=$1 LIMIT 1`, res.Belief.ID).Scan(&vprop) //nolint:errcheck
	pool.QueryRow(ctx, `SELECT note FROM belief_evidence WHERE belief_id=$1 LIMIT 1`, res.Belief.ID).Scan(&note)         //nolint:errcheck
	pool.QueryRow(ctx, `SELECT content FROM messages WHERE id=$1`, msgID).Scan(&msg)                                     //nolint:errcheck
	pool.QueryRow(ctx, `SELECT content FROM messages WHERE id=$1`, replyID).Scan(&reply)                                 //nolint:errcheck
	pool.QueryRow(ctx, `SELECT request::text, response::text FROM llm_calls WHERE id=$1`, callID).Scan(&req, &resp)      //nolint:errcheck
	if vprop != Marker || note != Marker || msg != Marker || reply != Marker || strings.Contains(req, "Leeds") || resp != nil {
		t.Fatalf("cascade content: version=%q note=%q msg=%q reply=%q req=%q", vprop, note, msg, reply, req)
	}
	if a, _ := as.List(ctx, m.ID, epistemics.Filter{Limit: 5}); len(a) != 1 || a[0].Proposition != Marker || a[0].ID != assertion.ID {
		t.Fatalf("assertion: %+v", a)
	}
	// Nothing in the live database still holds the secret.
	var leaks int
	pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM cognitive_events WHERE mind_id=$1 AND (summary ILIKE '%leeds%' OR payload::text ILIKE '%leeds%' OR metadata::text ILIKE '%leeds%'))
		+ (SELECT count(*) FROM memories WHERE mind_id=$1 AND content ILIKE '%leeds%') + (SELECT count(*) FROM messages WHERE mind_id=$1 AND content ILIKE '%leeds%')
		+ (SELECT count(*) FROM llm_calls WHERE mind_id=$1 AND request::text ILIKE '%leeds%') + (SELECT count(*) FROM assertions WHERE mind_id=$1 AND (proposition ILIKE '%leeds%' OR context ILIKE '%leeds%'))`, m.ID).Scan(&leaks) //nolint:errcheck
	if leaks != 0 {
		t.Fatalf("secret still present in %d rows", leaks)
	}
	// Stored archives were scrubbed and re-signed; they still validate.
	a := mustArchive(t, snaps, sn.ID)
	if raw, _ := json.Marshal(a); strings.Contains(string(raw), "Leeds") || strings.Contains(string(raw), "steals") {
		t.Fatal("archive still holds redacted content")
	}
	if len(a.Redacted) < 3 || snapshots.Validate(a) != nil {
		t.Fatalf("archive scrub record or checksum: %v %v", a.Redacted, snapshots.Validate(a))
	}
	// The ledger and the history record what happened, not what was removed.
	ledger, _ := svc.List(ctx, m.ID, 20)
	if len(ledger) != 1+len(cascaded) || ledger[0].Reason != "data subject request" {
		t.Fatalf("ledger: %d", len(ledger))
	}
	reds, _ := es.Query(ctx, evstore.Filter{MindID: m.ID, Types: []events.Type{events.Redaction}, Limit: 20})
	if len(reds) != 1+len(cascaded) || strings.Contains(reds[0].Summary, "Leeds") {
		t.Fatalf("redaction events: %d", len(reds))
	}
	// Idempotent and still append-only for everything else.
	if _, _, err := svc.Redact(ctx, Request{MindID: m.ID, Kind: KindEvent, ID: obs[0].ID, Reason: "again", By: "admin"}); err == nil {
		t.Fatal("second redaction should report already redacted")
	}
	if _, err := pool.Exec(ctx, `UPDATE cognitive_events SET summary='tampered' WHERE id=$1`, child[0].ID); err == nil {
		t.Fatal("plain updates must still be forbidden")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM cognitive_events WHERE id=$1`, obs[0].ID); err == nil {
		t.Fatal("deletes must still be forbidden, even of redacted rows")
	}
	if _, err := pool.Exec(ctx, `UPDATE redactions SET reason='x' WHERE id=$1`, primary.ID); err == nil {
		t.Fatal("the ledger is append-only")
	}
}

func mustArchive(t *testing.T, s *snapshots.Store, id ids.ID) snapshots.Archive {
	t.Helper()
	a, err := s.Archive(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func mustJSONB(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) []byte {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
