package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/graph"
)

// GitPoller mirrors a repository (bare, shallow) and turns new commits into
// observations. It runs git with argument vectors only, disables hooks and
// submodule recursion, and never checks out a working tree (threats T8, T23).
type GitPoller struct {
	DataDir string        // where mirrors live
	Timeout time.Duration // per git command
}

func (g GitPoller) git(ctx context.Context, dir string, args ...string) (string, error) {
	t := g.Timeout
	if t <= 0 {
		t = 2 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=always"}, args...)
	cmd := exec.CommandContext(cctx, "git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, truncateStr(string(out), 300))
	}
	return string(out), nil
}

// Poll implements Poller.
func (g GitPoller) Poll(ctx context.Context, in Integration, sink *Sink) error {
	url, _ := in.Config["url"].(string)
	branch, _ := in.Config["branch"].(string)
	maxCommits := 50
	if n, ok := in.Config["max_commits"].(float64); ok && n > 0 && n <= 500 {
		maxCommits = int(n)
	}
	sum := sha256.Sum256([]byte(url))
	dir := filepath.Join(g.DataDir, "git", hex.EncodeToString(sum[:8])+".git")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			return err
		}
		if _, err := g.git(ctx, "", "clone", "--bare", "--depth", "200", "--no-recurse-submodules", "--", url, dir); err != nil {
			return err
		}
		sink.Note("cloned mirror")
	} else if _, err := g.git(ctx, dir, "fetch", "--depth", "200", "--no-recurse-submodules", "--prune", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
		return err
	}
	ref := "HEAD"
	if branch != "" {
		ref = strings.TrimPrefix(branch, "refs/heads/")
	}
	last, _ := in.State["last_commit"].(string)
	rangeSpec := ref
	if last != "" {
		rangeSpec = last + ".." + ref
	}
	out, err := g.git(ctx, dir, "log", "--no-merges", "--date=iso-strict", "--format=%x1e%H%x1f%an%x1f%ae%x1f%ad%x1f%s", "--name-only", "-n", strconv.Itoa(maxCommits), rangeSpec, "--")
	if err != nil {
		if last == "" {
			return err
		}
		// The last commit may have vanished (force push): start afresh from the tip.
		out, err = g.git(ctx, dir, "log", "--no-merges", "--date=iso-strict", "--format=%x1e%H%x1f%an%x1f%ae%x1f%ad%x1f%s", "--name-only", "-n", strconv.Itoa(maxCommits), ref, "--")
		if err != nil {
			return err
		}
		sink.Note("history rewritten upstream; re-read from tip")
	}
	commits := parseLog(out)
	if len(commits) == 0 {
		return nil
	}
	repoName := repoLabel(url)
	// Oldest first so state advances monotonically.
	sort.SliceStable(commits, func(i, j int) bool { return i > j })
	coChange := loadCoChange(in.State)
	authors := map[string]int{}
	for _, c := range commits {
		sink.Item()
		authors[c.author]++
		text := fmt.Sprintf("commit %s by %s at %s\n%s\nfiles: %s", c.hash[:12], c.author, c.date, c.subject, strings.Join(c.files, ", "))
		if _, err := sink.Observe(ctx, Observation{Summary: "commit " + c.hash[:8] + ": " + c.subject, Text: text, Reference: url + "#" + c.hash, ContentType: "text/plain", Salience: commitSalience(c),
			Metadata: map[string]any{"commit": c.hash, "author": c.author, "files": len(c.files), "repository": repoName}, Entities: append([]string{repoName, c.author}, nil...), EntityKind: graph.Repository}); err != nil {
			sink.Fail(err)
			continue
		}
		for i := 0; i < len(c.files); i++ {
			for j := i + 1; j < len(c.files) && j < i+15; j++ {
				a, b := c.files[i], c.files[j]
				if a > b {
					a, b = b, a
				}
				coChange[a+" + "+b]++
			}
		}
	}
	state := map[string]any{"last_commit": commits[len(commits)-1].hash, "last_poll": time.Now().Format(time.RFC3339), "commits_seen": intFrom(in.State["commits_seen"]) + len(commits), "co_change": topPairs(coChange, 30)}
	if err := sink.Store.SetState(ctx, in.ID, state); err != nil {
		return err
	}
	sink.Note("%d new commits from %d author(s)", len(commits), len(authors))
	return nil
}

type commit struct {
	hash, author, email, date, subject string
	files                              []string
}

func parseLog(out string) []commit {
	var cs []commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		head, filesPart, _ := strings.Cut(rec, "\n")
		parts := strings.Split(head, "\x1f")
		if len(parts) < 5 {
			continue
		}
		c := commit{hash: parts[0], author: parts[1], email: parts[2], date: parts[3], subject: truncateStr(parts[4], 200)}
		for _, f := range strings.Split(filesPart, "\n") {
			if f = strings.TrimSpace(f); f != "" && len(c.files) < 200 {
				c.files = append(c.files, f)
			}
		}
		cs = append(cs, c)
	}
	return cs
}

func commitSalience(c commit) float64 {
	s := 0.4
	l := strings.ToLower(c.subject)
	for _, w := range []string{"fix", "revert", "hotfix", "security", "breaking", "outage", "incident"} {
		if strings.Contains(l, w) {
			s = 0.65
			break
		}
	}
	if len(c.files) > 20 {
		s += 0.1
	}
	return s
}

func repoLabel(url string) string {
	u := strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 && i < len(u)-1 {
		parts := strings.Split(u, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
		return u[i+1:]
	}
	return u
}

func loadCoChange(state map[string]any) map[string]int {
	out := map[string]int{}
	if raw, ok := state["co_change"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				out[fmt.Sprint(m["pair"])] = intFrom(m["count"])
			}
		}
	}
	return out
}

func topPairs(m map[string]int, n int) []map[string]any {
	type kv struct {
		k string
		v int
	}
	var all []kv
	for k, v := range m {
		if v >= 2 {
			all = append(all, kv{k, v})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make([]map[string]any, 0, len(all))
	for _, x := range all {
		out = append(out, map[string]any{"pair": x.k, "count": x.v})
	}
	return out
}

func intFrom(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// ReadFile returns a file at a ref from a mirrored repository (read-only,
// bounded). Used by the repository.read capability.
func (g GitPoller) ReadFile(ctx context.Context, url, ref, file string, max int) (string, error) {
	if strings.Contains(file, "..") || strings.HasPrefix(file, "/") || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("unsafe path or ref")
	}
	sum := sha256.Sum256([]byte(url))
	dir := filepath.Join(g.DataDir, "git", hex.EncodeToString(sum[:8])+".git")
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("repository is not mirrored yet")
	}
	if ref == "" {
		ref = "HEAD"
	}
	out, err := g.git(ctx, dir, "show", ref+":"+file)
	if err != nil {
		return "", err
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out, nil
}
