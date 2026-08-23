// Package snapshots captures a mind's state as a self-describing archive,
// restores archives into new minds (fork, import) and compares minds.
//
// An archive is JSON: identity, personality, configuration and the rows of
// every mind-scoped state table. Cognitive history (events) is not copied: a
// fork shares it by reference through forked_from_snapshot → event_seq. Every
// id inside an archive is remapped on restore, so an archive can never
// overwrite existing rows, and imported archives are treated as hostile:
// bounded size, allow-listed tables, checksum, no secrets.
package snapshots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/identity"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// Kind and Version identify the archive format.
const (
	Kind    = "noema.mind-archive"
	Version = 1
	// MaxArchiveBytes bounds what import will parse.
	MaxArchiveBytes = 32 << 20
	// MaxRows bounds rows per table on import.
	MaxRows = 200_000
)

// Tables lists the state tables copied, in insertion order (parents before
// children; rows within a table are ordered by id, and UUIDv7 ids are
// time-ordered, so self-references resolve). Anything not listed (events,
// llm_calls, approvals, integrations, job logs, notifications) stays with
// the original mind.
var Tables = []string{
	"workspace_states", "self_models", "drives", "affect_states",
	"goals", "beliefs", "belief_evidence", "belief_alternatives", "belief_versions", "belief_links",
	"memories", "memory_relationships", "predictions", "prediction_results",
	"entities", "entity_relationships", "entity_links", "corrections", "policies",
	"conversations", "messages",
}

// keyedByMind are tables whose primary key includes mind_id; a fresh mind may
// already hold a row, so restore upserts them.
var keyedByMind = map[string]string{"workspace_states": "mind_id", "self_models": "mind_id", "affect_states": "mind_id", "drives": "mind_id, name"}

// Archive is the portable form of a mind's state.
type Archive struct {
	Kind      string    `json:"kind"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Source    Source    `json:"source"`
	Mind      MindInfo  `json:"mind"`
	// Tables maps table name → rows (column → value, as JSON).
	Tables   map[string][]map[string]any `json:"tables"`
	Checksum string                      `json:"checksum"`
	// Redacted lists row ids whose content was removed from this archive
	// after capture under a privacy redaction; the checksum covers the
	// scrubbed content.
	Redacted []string `json:"redacted,omitempty"`
}

// Source records where an archive came from.
type Source struct {
	MindID   ids.ID `json:"mind_id"`
	Slug     string `json:"slug"`
	EventSeq int64  `json:"event_seq"`
	Noema    string `json:"noema"`
}

// MindInfo carries the identity part of an archive.
type MindInfo struct {
	Slug               string                `json:"slug"`
	DisplayName        string                `json:"display_name"`
	Notes              string                `json:"notes"`
	Status             string                `json:"status"`
	PersonalityVersion int                   `json:"personality_version"`
	Personality        minds.Personality     `json:"personality"`
	Config             minds.CognitiveConfig `json:"config"`
}

// Counts summarises an archive.
func (a Archive) Counts() map[string]int {
	out := map[string]int{}
	for t, rows := range a.Tables {
		if len(rows) > 0 {
			out[t] = len(rows)
		}
	}
	return out
}

// canonical returns the JSON the checksum covers (everything but the checksum).
func (a Archive) canonical() []byte {
	c := a
	c.Checksum = ""
	b, _ := json.Marshal(c) // encoding/json sorts map keys, so this is stable
	return b
}

// Sum computes the archive checksum.
func Sum(a Archive) string {
	h := sha256.Sum256(a.canonical())
	return "sha256:" + hex.EncodeToString(h[:])
}

// ErrInvalid marks an archive that fails validation.
var ErrInvalid = errors.New("snapshots: invalid archive")

var tableSet = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range Tables {
		m[t] = true
	}
	return m
}()

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Validate checks structure, bounds and checksum. It does not trust anything
// inside rows; restore remaps every id and forces mind_id.
func Validate(a Archive) error {
	if a.Kind != Kind {
		return fmt.Errorf("%w: kind %q", ErrInvalid, a.Kind)
	}
	if a.Version != Version {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalid, a.Version)
	}
	if a.Checksum == "" || a.Checksum != Sum(a) {
		return fmt.Errorf("%w: checksum mismatch", ErrInvalid)
	}
	if !slugRe.MatchString(a.Mind.Slug) {
		return fmt.Errorf("%w: bad slug", ErrInvalid)
	}
	total := 0
	for t, rows := range a.Tables {
		if !tableSet[t] {
			return fmt.Errorf("%w: unknown table %q", ErrInvalid, t)
		}
		if len(rows) > MaxRows {
			return fmt.Errorf("%w: table %s has too many rows", ErrInvalid, t)
		}
		total += len(rows)
		for _, r := range rows {
			for k := range r {
				if !colRe.MatchString(k) {
					return fmt.Errorf("%w: bad column name in %s", ErrInvalid, t)
				}
			}
		}
	}
	if total > 4*MaxRows {
		return fmt.Errorf("%w: too many rows", ErrInvalid)
	}
	return nil
}

var colRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Capture builds an archive from the live state of a mind.
func Capture(ctx context.Context, db *pgxpool.Pool, es *evstore.Store, m minds.Mind) (Archive, error) {
	a := Archive{Kind: Kind, Version: Version, CreatedAt: time.Now().UTC(), Tables: map[string][]map[string]any{},
		Source: Source{MindID: m.ID, Slug: m.Slug, Noema: "noema/1"},
		Mind:   MindInfo{Slug: m.Slug, DisplayName: m.DisplayName, Notes: m.Notes, Status: string(m.Status), PersonalityVersion: m.PersonalityVersion, Personality: m.Personality, Config: m.Config}}
	if last, err := es.Query(ctx, evstore.Filter{MindID: m.ID, Limit: 1}); err == nil && len(last) > 0 {
		a.Source.EventSeq = last[0].Seq
	}
	cols, err := columns(ctx, db)
	if err != nil {
		return a, err
	}
	for _, t := range Tables {
		cs := cols[t]
		if len(cs) == 0 {
			continue
		}
		order := "mind_id"
		if contains(cs, "id") {
			order = "id"
		} else if t == "drives" {
			order = "name"
		}
		rows, err := db.Query(ctx, fmt.Sprintf(`SELECT to_jsonb(t) - 'search' FROM %s t WHERE mind_id=$1 ORDER BY %s`, t, order), m.ID)
		if err != nil {
			return a, fmt.Errorf("capture %s: %w", t, err)
		}
		var out []map[string]any
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return a, err
			}
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				rows.Close()
				return a, err
			}
			out = append(out, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return a, err
		}
		if out != nil {
			a.Tables[t] = out
		}
	}
	a.Checksum = Sum(a)
	return a, nil
}

// columns returns insertable (non-generated) columns for every archive table.
func columns(ctx context.Context, db *pgxpool.Pool) (map[string][]string, error) {
	rows, err := db.Query(ctx, `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema='public' AND is_generated='NEVER' AND table_name = ANY($1) ORDER BY table_name, ordinal_position`, Tables)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			return nil, err
		}
		out[t] = append(out[t], c)
	}
	return out, rows.Err()
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// AuthorityTables hold rows that grant or shape authority rather than
// describe cognitive state. They are copied by Fork (same operator, same
// instance) but never by Import: an archive from elsewhere must not be able
// to arrive carrying ALLOW rules for a mind (THREAT_MODEL T16).
var AuthorityTables = map[string]bool{"policies": true}

// Restore writes an archive's rows into mind `into`. Every row id found in
// the archive gets a fresh id and all references to it (any uuid-shaped
// string anywhere in the rows, including arrays and JSON detail) follow.
// References to ids not in the archive (events, templates) are kept: that
// is how history stays shared by reference. Must run in the caller's
// transaction after the mind row exists. Tables named in skip are not
// written; the returned map reports how many rows were omitted per table.
func Restore(ctx context.Context, tx pgx.Tx, cols map[string][]string, a Archive, into ids.ID, skip map[string]bool) (map[string]int, error) {
	omitted := map[string]int{}
	remap := map[string]string{a.Source.MindID.String(): into.String()}
	for _, t := range Tables {
		for _, r := range a.Tables[t] {
			if id, ok := r["id"].(string); ok && uuidRe.MatchString(id) {
				if _, seen := remap[id]; !seen {
					remap[id] = ids.New().String()
				}
			}
		}
	}
	for _, t := range Tables {
		rows := a.Tables[t]
		if len(rows) == 0 {
			continue
		}
		if skip[t] {
			omitted[t] = len(rows)
			continue
		}
		cs := cols[t]
		if len(cs) == 0 {
			return omitted, fmt.Errorf("%w: table %s not present", ErrInvalid, t)
		}
		conflict := ""
		if key, ok := keyedByMind[t]; ok {
			var sets []string
			for _, c := range cs {
				if c != "mind_id" && c != "name" {
					sets = append(sets, c+"=EXCLUDED."+c)
				}
			}
			conflict = fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s", key, strings.Join(sets, ", "))
		}
		colList := strings.Join(cs, ", ")
		sql := fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM jsonb_populate_record(NULL::%s, $1)%s`, t, colList, colList, t, conflict)
		for _, r := range rows {
			row := remapValue(r, remap).(map[string]any)
			row["mind_id"] = into.String()
			b, _ := json.Marshal(row)
			if _, err := tx.Exec(ctx, sql, b); err != nil {
				return omitted, fmt.Errorf("restore %s: %w", t, err)
			}
		}
	}
	return omitted, nil
}

func remapValue(v any, remap map[string]string) any {
	switch x := v.(type) {
	case string:
		if n, ok := remap[x]; ok {
			return n
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = remapValue(e, remap)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = remapValue(e, remap)
		}
		return out
	}
	return v
}

// Snapshot is a stored archive.
type Snapshot struct {
	ID        ids.ID            `json:"id"`
	MindID    ids.ID            `json:"mind_id"`
	Name      string            `json:"name"`
	Note      string            `json:"note"`
	EventSeq  int64             `json:"event_seq"`
	Checksum  string            `json:"checksum"`
	SizeBytes int               `json:"size_bytes"`
	Counts    map[string]int    `json:"counts"`
	CreatedBy string            `json:"created_by"`
	CreatedAt time.Time         `json:"created_at"`
	Identity  identity.Identity `json:"identity"` // the runtime that captured it
}

// Store persists snapshots and performs forks and imports.
type Store struct {
	DB       *pgxpool.Pool
	Events   *evstore.Store
	Minds    *minds.Store
	Identity *identity.Capturer // nil records build information only
}

// Create captures and stores a snapshot.
func (s *Store) Create(ctx context.Context, m minds.Mind, name, note, by string) (Snapshot, error) {
	a, err := Capture(ctx, s.DB, s.Events, m)
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = m.Slug + " @ " + a.CreatedAt.Format("2006-01-02 15:04")
	}
	raw, _ := json.Marshal(a)
	counts, _ := json.Marshal(a.Counts())
	sn := Snapshot{ID: ids.New(), MindID: m.ID, Name: name, Note: note, EventSeq: a.Source.EventSeq, Checksum: a.Checksum, SizeBytes: len(raw), Counts: a.Counts(), CreatedBy: by, CreatedAt: time.Now(), Identity: s.Identity.Capture(ctx, m.ID)}
	idj, _ := json.Marshal(sn.Identity)
	_, err = s.DB.Exec(ctx, `INSERT INTO snapshots (id, mind_id, name, note, event_seq, checksum, size_bytes, counts, archive, created_by, created_at, identity) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		sn.ID, sn.MindID, sn.Name, sn.Note, sn.EventSeq, sn.Checksum, sn.SizeBytes, counts, raw, sn.CreatedBy, sn.CreatedAt, idj)
	return sn, err
}

const snapCols = `id, mind_id, name, note, event_seq, checksum, size_bytes, counts, created_by, created_at, identity`

func scanSnap(row pgx.Row) (Snapshot, error) {
	var sn Snapshot
	var counts, idj []byte
	if err := row.Scan(&sn.ID, &sn.MindID, &sn.Name, &sn.Note, &sn.EventSeq, &sn.Checksum, &sn.SizeBytes, &counts, &sn.CreatedBy, &sn.CreatedAt, &idj); err != nil {
		return sn, storage.Translate(err)
	}
	json.Unmarshal(counts, &sn.Counts) //nolint:errcheck
	json.Unmarshal(idj, &sn.Identity)  //nolint:errcheck
	return sn, nil
}

// Get returns snapshot metadata.
func (s *Store) Get(ctx context.Context, id ids.ID) (Snapshot, error) {
	return scanSnap(s.DB.QueryRow(ctx, `SELECT `+snapCols+` FROM snapshots WHERE id=$1`, id))
}

// Archive loads the stored archive of a snapshot.
func (s *Store) Archive(ctx context.Context, id ids.ID) (Archive, error) {
	var raw []byte
	if err := s.DB.QueryRow(ctx, `SELECT archive FROM snapshots WHERE id=$1`, id).Scan(&raw); err != nil {
		return Archive{}, storage.Translate(err)
	}
	var a Archive
	return a, json.Unmarshal(raw, &a)
}

// List returns snapshots for a mind (or all when mind is Nil), newest first.
func (s *Store) List(ctx context.Context, mind ids.ID, limit int) ([]Snapshot, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT `+snapCols+` FROM snapshots WHERE ($1 = '00000000-0000-0000-0000-000000000000'::uuid OR mind_id=$1) ORDER BY created_at DESC LIMIT $2`, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		sn, err := scanSnap(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

// Delete removes a snapshot that nothing references.
func (s *Store) Delete(ctx context.Context, id ids.ID) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM snapshots WHERE id=$1`, id)
	return storage.Translate(err)
}

// ForkOptions shape a restore into a new mind.
type ForkOptions struct {
	Slug, DisplayName, Notes string
	Status                   minds.Status // default active
	Config                   *minds.CognitiveConfig
	Personality              *minds.Personality
	By                       string
	// OmitAuthority skips AuthorityTables (policies). Import sets it; Fork does not.
	OmitAuthority bool
	// Omitted is filled by the restore with the rows skipped per table.
	Omitted map[string]int
}

// Fork creates a new mind from a snapshot. The new mind's state equals the
// snapshot; its history starts empty and links back through
// forked_from_snapshot to the shared past.
func (s *Store) Fork(ctx context.Context, snapshotID ids.ID, o ForkOptions) (minds.Mind, error) {
	a, err := s.Archive(ctx, snapshotID)
	if err != nil {
		return minds.Mind{}, err
	}
	m, err := s.restoreNew(ctx, a, &o)
	if err != nil {
		return m, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE minds SET forked_from_mind=$2, forked_from_snapshot=$3 WHERE id=$1`, m.ID, a.Source.MindID, snapshotID); err != nil {
		return m, err
	}
	return s.Minds.Get(ctx, m.ID)
}

// Import validates a hostile archive and restores it into a new mind.
// Authority-bearing tables are never imported: an archive is data about a
// mind, not a grant of permissions on this instance. The number of skipped
// rows is reported in o.Omitted when the caller passes a pointer-free copy;
// use ImportReport to receive it.
func (s *Store) Import(ctx context.Context, a Archive, o ForkOptions) (minds.Mind, error) {
	m, _, err := s.ImportReport(ctx, a, o)
	return m, err
}

// ImportReport is Import that also returns the rows omitted per table.
func (s *Store) ImportReport(ctx context.Context, a Archive, o ForkOptions) (minds.Mind, map[string]int, error) {
	if err := Validate(a); err != nil {
		return minds.Mind{}, nil, err
	}
	o.OmitAuthority = true
	m, err := s.restoreNew(ctx, a, &o)
	return m, o.Omitted, err
}

func (s *Store) restoreNew(ctx context.Context, a Archive, o *ForkOptions) (minds.Mind, error) {
	cfg := a.Mind.Config
	if o.Config != nil {
		cfg = *o.Config
	}
	cfg.Normalise()
	p := a.Mind.Personality
	if o.Personality != nil {
		p = *o.Personality
	}
	if o.Slug == "" {
		o.Slug = a.Mind.Slug
	}
	if o.DisplayName == "" {
		o.DisplayName = o.Slug
	}
	p.Identity.Name, p.Identity.DisplayName = o.DisplayName, o.DisplayName
	notes := o.Notes
	if notes == "" {
		notes = "restored from archive of " + a.Source.Slug + " at event " + fmt.Sprint(a.Source.EventSeq)
	}
	m, err := s.Minds.Create(ctx, minds.CreateParams{Slug: o.Slug, DisplayName: o.DisplayName, Personality: &p, Config: &cfg, Notes: notes, CreatedBy: o.By})
	if err != nil {
		return minds.Mind{}, err
	}
	cols, err := columns(ctx, s.DB)
	if err != nil {
		return m, err
	}
	var skip map[string]bool
	if o.OmitAuthority {
		skip = AuthorityTables
	}
	err = storage.Tx(ctx, s.DB, func(tx pgx.Tx) error {
		omitted, err := Restore(ctx, tx, cols, a, m.ID, skip)
		o.Omitted = omitted
		if err != nil {
			return err
		}
		if o.Status != "" && o.Status != minds.StatusActive {
			if _, err := tx.Exec(ctx, `UPDATE minds SET status=$2 WHERE id=$1`, m.ID, string(o.Status)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Leave no half-restored mind behind.
		s.DB.Exec(ctx, `UPDATE minds SET status='archived', notes=notes||' (restore failed: '||$2||')' WHERE id=$1`, m.ID, err.Error()) //nolint:errcheck
		return m, err
	}
	return s.Minds.Get(ctx, m.ID)
}

// SortedTables returns table names present in an archive, for display.
func SortedTables(a Archive) []string {
	var out []string
	for t := range a.Tables {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Scrub removes the content of a redacted row from every stored archive of
// the mind, recording the id in the archive and re-signing it. Returns the
// number of archives changed. Events and model calls are never in archives.
func (s *Store) Scrub(ctx context.Context, mind ids.ID, kind string, id ids.ID) (int, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, archive FROM snapshots WHERE mind_id=$1`, mind)
	if err != nil {
		return 0, err
	}
	type snap struct {
		id  ids.ID
		raw []byte
	}
	var snaps []snap
	for rows.Next() {
		var sn snap
		if err := rows.Scan(&sn.id, &sn.raw); err != nil {
			rows.Close()
			return 0, err
		}
		snaps = append(snaps, sn)
	}
	rows.Close()
	changed := 0
	target := id.String()
	for _, sn := range snaps {
		var a Archive
		if err := json.Unmarshal(sn.raw, &a); err != nil {
			continue
		}
		if !scrubArchive(&a, kind, target) {
			continue
		}
		a.Redacted = append(a.Redacted, target)
		a.Checksum = Sum(a)
		raw, _ := json.Marshal(a)
		counts, _ := json.Marshal(a.Counts())
		if _, err := s.DB.Exec(ctx, `UPDATE snapshots SET archive=$2, checksum=$3, size_bytes=$4, counts=$5 WHERE id=$1`, sn.id, raw, a.Checksum, len(raw), counts); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// scrubArchive replaces the content fields of the matching rows in place.
func scrubArchive(a *Archive, kind, id string) bool {
	changed := false
	replace := func(table string, match func(r map[string]any) bool, fields map[string]any) {
		for _, r := range a.Tables[table] {
			if match(r) {
				for k, v := range fields {
					if _, ok := r[k]; ok {
						r[k] = v
					}
				}
				changed = true
			}
		}
	}
	byID := func(r map[string]any) bool { return r["id"] == id }
	switch kind {
	case "memory":
		replace("memories", byID, map[string]any{"content": "[redacted]", "detail": map[string]any{"redacted": true}})
	case "message":
		replace("messages", byID, map[string]any{"content": "[redacted]", "intent": nil})
	case "belief":
		replace("beliefs", byID, map[string]any{"proposition": "[redacted]", "detail": map[string]any{"redacted": true}})
		byBelief := func(r map[string]any) bool { return r["belief_id"] == id }
		replace("belief_versions", byBelief, map[string]any{"proposition": "[redacted]", "reason": "[redacted]"})
		replace("belief_evidence", byBelief, map[string]any{"note": "[redacted]"})
		replace("belief_alternatives", byBelief, map[string]any{"proposition": "[redacted]", "note": "[redacted]"})
	}
	return changed
}
