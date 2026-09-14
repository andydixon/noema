// Package host provides typed, bounded host and service capabilities:
// disk and memory status, a process list without command lines, systemd
// unit status and restart, and journal queries with personal data scrubbed.
// Every operation runs a fixed argument vector without a shell, validates
// unit names, bounds time and output, and says what it changes. There is no
// general command capability here, and none can be added at runtime.
package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/capabilities"
)

// Runner executes one program with arguments and no shell. The daemon uses
// ExecRunner; tests substitute a fake so no real unit is touched.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs programs from a fixed set of absolute paths with a minimal
// environment and a bounded output.
type ExecRunner struct {
	MaxOutput int // bytes kept from stdout (default 256 KiB)
}

// allowedPrograms are the only binaries a host capability may start.
var allowedPrograms = map[string]bool{"systemctl": true, "journalctl": true}

// Run implements Runner.
func (e ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !allowedPrograms[name] {
		return nil, fmt.Errorf("host: %s is not an allowed program", name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("host: %s not available: %w", name, err)
	}
	max := e.MaxOutput
	if max <= 0 {
		max = 256 << 10
	}
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- program from a fixed allow-list; arguments validated by each capability
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "SYSTEMD_PAGER=", "SYSTEMD_COLORS=0"}
	var out limited
	out.max = max
	var stderr limited
	stderr.max = 4096
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), errors.New(msg)
	}
	return out.Bytes(), nil
}

type limited struct {
	bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.Len(); room > 0 {
		if len(p) > room {
			l.Buffer.Write(p[:room])
		} else {
			l.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// Options configure the host capabilities.
type Options struct {
	Runner   Runner
	ProcRoot string // default /proc
}

var unitRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@_.:-]{0,127}$`)

// Unit validates a systemd unit name and appends ".service" when no type is given.
func Unit(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !unitRe.MatchString(name) || strings.Contains(name, "..") {
		return "", errors.New("invalid unit name")
	}
	if !strings.Contains(name, ".") {
		name += ".service"
	}
	return name, nil
}

func unitTarget(params map[string]any) (string, error) {
	s, _ := params["service"].(string)
	return Unit(s)
}

// Capabilities returns the host capability set.
func Capabilities(o Options) []capabilities.Capability {
	if o.ProcRoot == "" {
		o.ProcRoot = "/proc"
	}
	run := o.Runner
	svc := map[string]capabilities.Param{"service": {Type: "string", Required: true, Description: "systemd unit, e.g. atlas-worker or atlas-worker.service", MaxLen: 128}}
	return []capabilities.Capability{
		{Name: "host.disk.usage", Description: "Report size, used and free space of the filesystem holding a path.", Risk: capabilities.RiskLow, TargetKind: "path", Access: capabilities.AccessRead, Idempotent: true, Timeout: 5 * time.Second,
			SideEffects: "none", DefaultEffect: capabilities.Allow,
			Params:     map[string]capabilities.Param{"path": {Type: "path", Description: "absolute path (default /)", MaxLen: 512}},
			TargetFrom: func(p map[string]any) (string, error) { return diskPath(p) },
			Executor: func(_ context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
				path, err := diskPath(inv.Params)
				if err != nil {
					return capabilities.Result{}, err
				}
				total, free, err := statfs(path)
				if err != nil {
					return capabilities.Result{}, err
				}
				used := total - free
				pct := 0.0
				if total > 0 {
					pct = float64(used) / float64(total) * 100
				}
				return capabilities.Result{Summary: fmt.Sprintf("%s: %.1f%% used (%d of %d bytes free)", path, pct, free, total),
					Data: map[string]any{"path": path, "total_bytes": total, "free_bytes": free, "used_bytes": used, "used_percent": round1(pct)}}, nil
			}},
		{Name: "host.memory.status", Description: "Report total, available and swap memory from the kernel.", Risk: capabilities.RiskLow, TargetKind: "none", Access: capabilities.AccessRead, Idempotent: true, Timeout: 5 * time.Second,
			SideEffects: "none", DefaultEffect: capabilities.Allow,
			Executor: func(_ context.Context, _ capabilities.Invocation) (capabilities.Result, error) {
				m, err := meminfo(filepath.Join(o.ProcRoot, "meminfo"))
				if err != nil {
					return capabilities.Result{}, err
				}
				avail := m["MemAvailable"]
				pct := 0.0
				if m["MemTotal"] > 0 {
					pct = float64(m["MemTotal"]-avail) / float64(m["MemTotal"]) * 100
				}
				return capabilities.Result{Summary: fmt.Sprintf("memory %.1f%% in use, %d KiB available", pct, avail),
					Data: map[string]any{"total_kib": m["MemTotal"], "available_kib": avail, "swap_total_kib": m["SwapTotal"], "swap_free_kib": m["SwapFree"], "used_percent": round1(pct)}}, nil
			}},
		{Name: "host.process.list", Description: "List processes by name, state and resident memory. Command lines are never read, because they can hold secrets.", Risk: capabilities.RiskLow, TargetKind: "none", Access: capabilities.AccessRead, Idempotent: true, Timeout: 10 * time.Second,
			SideEffects: "none", DefaultEffect: capabilities.Ask,
			Params: map[string]capabilities.Param{"match": {Type: "string", Description: "only names containing this text", MaxLen: 64}, "limit": {Type: "int", Description: "maximum rows (default 50, at most 200)"}},
			Executor: func(_ context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
				match, _ := inv.Params["match"].(string)
				limit, _ := inv.Params["limit"].(int)
				if limit <= 0 || limit > 200 {
					limit = 50
				}
				procs, err := processes(o.ProcRoot, match, limit)
				if err != nil {
					return capabilities.Result{}, err
				}
				return capabilities.Result{Summary: fmt.Sprintf("%d processes", len(procs)), Data: map[string]any{"processes": procs}}, nil
			}},
		{Name: "service.status", Description: "Report a systemd unit's load, active and sub state and main PID.", Risk: capabilities.RiskLow, TargetKind: "service", Access: capabilities.AccessRead, Idempotent: true, Timeout: 10 * time.Second,
			SideEffects: "none", DefaultEffect: capabilities.Allow, Params: svc, TargetFrom: unitTarget,
			Executor: func(ctx context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
				unit, err := unitTarget(inv.Params)
				if err != nil || run == nil {
					return capabilities.Result{}, errors.Join(err, errNoRunner(run))
				}
				out, err := run.Run(ctx, "systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID,ActiveEnterTimestamp,NRestarts", "--", unit)
				if err != nil {
					return capabilities.Result{}, err
				}
				props := parseProps(out)
				healthy := props["ActiveState"] == "active"
				return capabilities.Result{Summary: fmt.Sprintf("%s is %s (%s)", unit, props["ActiveState"], props["SubState"]), Data: map[string]any{"unit": unit, "properties": props, "active": healthy}}, nil
			}},
		{Name: "service.restart", Description: "Restart a systemd unit.", Risk: capabilities.RiskHigh, TargetKind: "service", Access: capabilities.AccessWrite, Idempotent: false, Timeout: 90 * time.Second,
			SideEffects: "stops and starts the unit: in-flight work and connections are dropped", DefaultEffect: capabilities.Ask, Params: svc, TargetFrom: unitTarget,
			Estimate: capabilities.Cost{WallSeconds: 10},
			Executor: func(ctx context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
				unit, err := unitTarget(inv.Params)
				if err != nil || run == nil {
					return capabilities.Result{}, errors.Join(err, errNoRunner(run))
				}
				if _, err := run.Run(ctx, "systemctl", "restart", "--no-block", "--", unit); err != nil {
					return capabilities.Result{}, err
				}
				// The command returning is not the service being healthy; a
				// plan verifies with service.status afterwards.
				return capabilities.Result{Summary: "restart of " + unit + " requested (health not yet verified)", Data: map[string]any{"unit": unit, "verified": false}}, nil
			}},
		{Name: "logs.query", Description: "Read recent journal lines for a unit. E-mail addresses, IP addresses and credential-like values are masked unless redact_pii is false.", Risk: capabilities.RiskMedium, TargetKind: "service", Access: capabilities.AccessRead, Idempotent: true, Timeout: 30 * time.Second,
			SideEffects: "none; the output may contain personal data", DefaultEffect: capabilities.Ask, TargetFrom: unitTarget,
			Params: map[string]capabilities.Param{"service": svc["service"], "since_minutes": {Type: "int", Description: "look back this many minutes (default 60, at most 1440)"}, "lines": {Type: "int", Description: "maximum lines (default 200, at most 1000)"},
				"grep": {Type: "string", Description: "only lines containing this text", MaxLen: 200}, "redact_pii": {Type: "bool", Description: "mask personal data (default true)"}},
			Estimate: capabilities.Cost{WallSeconds: 2},
			Executor: func(ctx context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
				unit, err := unitTarget(inv.Params)
				if err != nil || run == nil {
					return capabilities.Result{}, errors.Join(err, errNoRunner(run))
				}
				since, _ := inv.Params["since_minutes"].(int)
				if since <= 0 || since > 1440 {
					since = 60
				}
				lines, _ := inv.Params["lines"].(int)
				if lines <= 0 || lines > 1000 {
					lines = 200
				}
				out, err := run.Run(ctx, "journalctl", "--no-pager", "-o", "short-iso", "-u", unit, "--since", fmt.Sprintf("-%dmin", since), "-n", strconv.Itoa(lines))
				if err != nil {
					return capabilities.Result{}, err
				}
				text := string(out)
				if g, _ := inv.Params["grep"].(string); g != "" {
					var kept []string
					for _, l := range strings.Split(text, "\n") {
						if strings.Contains(l, g) {
							kept = append(kept, l)
						}
					}
					text = strings.Join(kept, "\n")
				}
				redact := true
				if v, ok := inv.Params["redact_pii"].(bool); ok {
					redact = v
				}
				masked := 0
				if redact {
					text, masked = Scrub(text)
				}
				n := 0
				if strings.TrimSpace(text) != "" {
					n = strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
				}
				return capabilities.Result{Summary: fmt.Sprintf("%d journal lines for %s over %d min (%d values masked)", n, unit, since, masked), Output: text,
					Data: map[string]any{"unit": unit, "lines": n, "since_minutes": since, "masked": masked, "redacted": redact}}, nil
			}},
	}
}

func errNoRunner(r Runner) error {
	if r == nil {
		return errors.New("host: no command runner configured")
	}
	return nil
}

func diskPath(p map[string]any) (string, error) {
	path, _ := p["path"].(string)
	if path == "" {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	return filepath.Clean(path), nil
}

func meminfo(file string) (map[string]int64, error) {
	f, err := os.Open(file) // #nosec G304 -- fixed kernel file under the configured proc root
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]int64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			out[k] = n
		}
	}
	if _, ok := out["MemTotal"]; !ok {
		return nil, errors.New("meminfo: MemTotal missing")
	}
	return out, sc.Err()
}

// Process is one row of host.process.list.
type Process struct {
	PID    int    `json:"pid"`
	Name   string `json:"name"`
	State  string `json:"state"`
	RSSKiB int64  `json:"rss_kib"`
}

func processes(root, match string, limit int) ([]Process, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(root, e.Name(), "stat")) // #nosec G304 -- numeric pid directory under the proc root
		if err != nil {
			continue
		}
		// pid (comm) state ... ; comm may contain spaces and parentheses.
		s := string(stat)
		open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || close < open {
			continue
		}
		name := s[open+1 : close]
		rest := strings.Fields(s[close+1:])
		if match != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(match)) {
			continue
		}
		p := Process{PID: pid, Name: name}
		if len(rest) > 0 {
			p.State = rest[0]
		}
		if len(rest) > 21 {
			if pages, err := strconv.ParseInt(rest[21], 10, 64); err == nil {
				p.RSSKiB = pages * int64(os.Getpagesize()) / 1024
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RSSKiB > out[j].RSSKiB })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func parseProps(out []byte) map[string]string {
	props := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			props[k] = v
		}
	}
	return props
}

var (
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	ipv4Re   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Re   = regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){3,7}[0-9a-fA-F]{1,4}\b`)
	secretRe = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|authorization)(\s*[=:]\s*|\s+)(bearer\s+)?\S+`)
)

// Scrub masks e-mail addresses, IP addresses and credential-like values and
// returns the text with the number of values masked. It is a mitigation, not
// a guarantee: free-text personal data (names, addresses) passes through.
func Scrub(text string) (string, int) {
	n := 0
	count := func(re *regexp.Regexp, repl func(string) string) {
		text = re.ReplaceAllStringFunc(text, func(m string) string { n++; return repl(m) })
	}
	count(secretRe, func(m string) string {
		parts := secretRe.FindStringSubmatch(m)
		return parts[1] + parts[2] + "[masked]"
	})
	count(emailRe, func(string) string { return "[email]" })
	count(ipv6Re, func(string) string { return "[ip]" })
	count(ipv4Re, func(string) string { return "[ip]" })
	return text, n
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
