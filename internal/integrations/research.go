package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/pkg/ids"
)

// ResearchPoller watches a folder. Files are fingerprinted, never executed,
// read within a size cap, and only text-like formats are extracted in
// process. Symlinks, archives and unknown binaries are recorded as rejected
// so the operator can see what was skipped (threats T3, T7).
type ResearchPoller struct {
	MaxBytes int64 // per file; default 5 MiB
	MaxFiles int   // per poll; default 200
}

var textExt = map[string]string{".txt": "text/plain", ".md": "text/markdown", ".markdown": "text/markdown", ".log": "text/plain", ".csv": "text/csv", ".json": "application/json",
	".yaml": "text/yaml", ".yml": "text/yaml", ".toml": "text/plain", ".ini": "text/plain", ".conf": "text/plain", ".go": "text/x-go", ".py": "text/x-python", ".js": "text/javascript",
	".ts": "text/typescript", ".sh": "text/x-shellscript", ".sql": "text/x-sql", ".html": "text/html", ".htm": "text/html", ".xml": "text/xml", ".rst": "text/plain", ".tex": "text/plain"}

var rejectExt = map[string]string{".zip": "archives are not extracted", ".tar": "archives are not extracted", ".gz": "archives are not extracted", ".tgz": "archives are not extracted", ".7z": "archives are not extracted",
	".rar": "archives are not extracted", ".exe": "executables are never processed", ".dll": "executables are never processed", ".so": "executables are never processed", ".bin": "binaries are not processed", ".iso": "images are not processed"}

// Poll implements Poller.
func (p ResearchPoller) Poll(ctx context.Context, in Integration, sink *Sink) error {
	root, _ := in.Config["path"].(string)
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("research folder: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("research folder: %s is not a directory", root)
	}
	maxBytes := p.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 5 << 20
	}
	maxFiles := p.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 200
	}
	fsys, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer fsys.Close()
	seen := 0
	err = fs.WalkDir(fsys.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." {
				return fs.SkipDir
			}
			if strings.Count(rel, string(filepath.Separator)) >= 5 {
				return fs.SkipDir
			}
			return nil
		}
		if seen >= maxFiles {
			return fs.SkipAll
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil // symlinks, devices and hidden files are ignored
		}
		seen++
		sink.Item()
		p.ingestFile(ctx, fsys, rel, root, maxBytes, in, sink)
		return nil
	})
	return err
}

func (p ResearchPoller) ingestFile(ctx context.Context, fsys *os.Root, rel, root string, maxBytes int64, in Integration, sink *Sink) {
	ref := filepath.Join(root, rel)
	ext := strings.ToLower(filepath.Ext(rel))
	doc := Document{MindID: sink.Mind, IntegrationID: in.ID, Reference: ref, Title: filepath.Base(rel)}
	st, err := fsys.Stat(rel)
	if err != nil {
		return
	}
	doc.SizeBytes = st.Size()
	// Cheap fingerprint for the duplicate check: hash of path-independent content requires reading; hash size+mtime+name first to skip unchanged files quickly.
	quick := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", rel, st.Size(), st.ModTime().UnixNano())))
	if sink.Store.HasFingerprint(ctx, sink.Mind, "q:"+hex.EncodeToString(quick[:16])) {
		return
	}
	if reason, bad := rejectExt[ext]; bad {
		doc.Status, doc.Error, doc.Fingerprint = "rejected", reason, "q:"+hex.EncodeToString(quick[:16])
		sink.Store.RecordDocument(ctx, doc) //nolint:errcheck
		sink.Note("rejected %s: %s", rel, reason)
		return
	}
	if st.Size() > maxBytes {
		doc.Status, doc.Error, doc.Fingerprint = "rejected", fmt.Sprintf("larger than %d bytes", maxBytes), "q:"+hex.EncodeToString(quick[:16])
		sink.Store.RecordDocument(ctx, doc) //nolint:errcheck
		sink.Note("rejected %s: too large", rel)
		return
	}
	f, err := fsys.Open(rel)
	if err != nil {
		sink.Fail(err)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		sink.Fail(err)
		return
	}
	sum := sha256.Sum256(data)
	doc.Fingerprint = hex.EncodeToString(sum[:])
	if sink.Store.HasFingerprint(ctx, sink.Mind, doc.Fingerprint) {
		// Same content seen (possibly under another name): record the quick key so we skip next time.
		sink.Store.RecordDocument(ctx, Document{MindID: sink.Mind, IntegrationID: in.ID, Fingerprint: "q:" + hex.EncodeToString(quick[:16]), Reference: ref, Title: doc.Title, Status: "duplicate", ContentType: textExt[ext], SizeBytes: st.Size()}) //nolint:errcheck
		return
	}
	ctype, textLike := textExt[ext]
	if !textLike {
		if ext == ".pdf" {
			ctype = "application/pdf"
			doc.Status, doc.Error = "ingested", "PDF text extraction is not built in; recorded as metadata only"
		} else if utf8.Valid(data) && !hasBinary(data) {
			ctype, textLike = "text/plain", true
		} else {
			doc.Status, doc.Error = "rejected", "unknown binary format"
		}
	}
	doc.ContentType = ctype
	var text string
	if textLike {
		text = string(data)
		if ctype == "text/html" {
			text = stripTags(text)
		}
		text = strings.ToValidUTF8(text, "")
		if len(text) > 200_000 {
			text = text[:200_000]
		}
		doc.ExtractedChars = len(text)
		doc.Status = "ingested"
	}
	doc.Excerpt = excerpt(text, 800)
	if doc.Status == "rejected" {
		sink.Store.RecordDocument(ctx, doc) //nolint:errcheck
		sink.Note("rejected %s: %s", rel, doc.Error)
		return
	}
	ev, err := sink.Observe(ctx, Observation{Summary: "document: " + doc.Title, Text: firstNonEmpty(excerpt(text, 6000), "(no text extracted from "+doc.Title+")"), Reference: ref, ContentType: ctype, Salience: 0.55,
		Metadata: map[string]any{"document": doc.Title, "fingerprint": doc.Fingerprint, "bytes": st.Size(), "chars": doc.ExtractedChars}, Entities: []string{doc.Title}, EntityKind: graph.Document})
	if err != nil {
		sink.Fail(err)
		return
	}
	doc.EventID = ev.ID
	sink.Store.RecordDocument(ctx, doc)                                                                                                                                                                                                    //nolint:errcheck
	sink.Store.RecordDocument(ctx, Document{MindID: sink.Mind, IntegrationID: in.ID, Fingerprint: "q:" + hex.EncodeToString(quick[:16]), Reference: ref, Title: doc.Title, Status: "duplicate", ContentType: ctype, SizeBytes: st.Size()}) //nolint:errcheck
	sink.Note("ingested %s (%d chars)", rel, doc.ExtractedChars)
	_ = ids.Nil
}

func hasBinary(b []byte) bool {
	n := 0
	for _, c := range b[:min(len(b), 4096)] {
		if c == 0 {
			return true
		}
		if c < 9 || (c > 13 && c < 32) {
			n++
		}
	}
	return n > 20
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n]
	}
	return s
}

// stripTags removes markup crudely; output is data, never rendered as HTML.
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
			b.WriteByte(' ')
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}
