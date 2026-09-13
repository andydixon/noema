package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/pkg/ids"
)

type fakeRunner struct {
	calls [][]string
	out   map[string]string
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return []byte(f.out[name]), f.err
}

func registry(t *testing.T, o Options) *capabilities.Registry {
	t.Helper()
	r := capabilities.NewRegistry()
	for _, c := range Capabilities(o) {
		r.Add(c)
	}
	r.Freeze()
	return r
}

func invoke(t *testing.T, r *capabilities.Registry, name, target string, params map[string]any) (capabilities.Result, error) {
	t.Helper()
	c, ok := r.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	p, err := c.ValidateParams(params)
	if err != nil {
		return capabilities.Result{}, err
	}
	if _, err := c.ResolveTarget(target, p); err != nil {
		return capabilities.Result{}, err
	}
	return c.Executor(context.Background(), capabilities.Invocation{MindID: ids.New(), Capability: name, Target: target, Params: p})
}

func TestUnitNames(t *testing.T) {
	for in, want := range map[string]string{"atlas-worker": "atlas-worker.service", "nginx.service": "nginx.service", "getty@tty1.service": "getty@tty1.service"} {
		if got, err := Unit(in); err != nil || got != want {
			t.Errorf("Unit(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "-H", "--root=/", "a b", "x;reboot", "../../etc", "a\\b", "$(id)", strings.Repeat("a", 200)} {
		if _, err := Unit(bad); err == nil {
			t.Errorf("Unit(%q) accepted", bad)
		}
	}
}

func TestServiceCapabilitiesUseFixedArgvAndDerivedTarget(t *testing.T) {
	f := &fakeRunner{out: map[string]string{"systemctl": "LoadState=loaded\nActiveState=failed\nSubState=failed\nMainPID=0\n", "journalctl": "07:14 atlas-worker: user alice@example.com from 10.1.2.3 failed, token=abc123\n07:15 queue stalled\n"}}
	r := registry(t, Options{Runner: f})
	res, err := invoke(t, r, "service.status", "", map[string]any{"service": "atlas-worker"})
	if err != nil || res.Data["active"] != false || !strings.Contains(res.Summary, "failed") {
		t.Fatalf("status: %v %+v", err, res)
	}
	if got := strings.Join(f.calls[0], " "); !strings.HasSuffix(got, "-- atlas-worker.service") || !strings.HasPrefix(got, "systemctl show") {
		t.Fatalf("argv: %s", got)
	}
	// A target that disagrees with the parameters is refused, so a policy on
	// one unit cannot be used to act on another.
	if _, err := invoke(t, r, "service.restart", "harmless.service", map[string]any{"service": "database"}); !errors.Is(err, capabilities.ErrInvalid) {
		t.Fatalf("mismatched target accepted: %v", err)
	}
	if _, err := invoke(t, r, "service.restart", "", map[string]any{"service": "--root=/"}); err == nil {
		t.Fatal("option injection accepted")
	}
	res, err = invoke(t, r, "service.restart", "atlas-worker.service", map[string]any{"service": "atlas-worker"})
	if err != nil || res.Data["verified"] != false {
		t.Fatalf("restart: %v %+v", err, res)
	}
	res, err = invoke(t, r, "logs.query", "", map[string]any{"service": "atlas-worker", "since_minutes": float64(30)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "alice@example.com") || strings.Contains(res.Output, "10.1.2.3") || strings.Contains(res.Output, "abc123") || res.Data["masked"].(int) != 3 {
		t.Fatalf("personal data not masked: %q %+v", res.Output, res.Data)
	}
	res, _ = invoke(t, r, "logs.query", "", map[string]any{"service": "atlas-worker", "redact_pii": false, "grep": "stalled"})
	if res.Output != "07:15 queue stalled" {
		t.Fatalf("grep: %q", res.Output)
	}
	restart, _ := r.Get("service.restart")
	status, _ := r.Get("service.status")
	if restart.Access != capabilities.AccessWrite || restart.Idempotent || restart.DefaultEffect != capabilities.Ask || status.Access != capabilities.AccessRead {
		t.Fatal("service metadata wrong")
	}
	// No runner: fail, never silently succeed.
	if _, err := invoke(t, registry(t, Options{}), "service.status", "", map[string]any{"service": "x"}); err == nil {
		t.Fatal("status without a runner succeeded")
	}
}

func TestExecRunnerRefusesOtherPrograms(t *testing.T) {
	if _, err := (ExecRunner{}).Run(context.Background(), "sh", "-c", "id"); err == nil {
		t.Fatal("ExecRunner ran a shell")
	}
}

func TestProcAndMemory(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "meminfo"), []byte("MemTotal:       1000 kB\nMemAvailable:    250 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"), 0o600) //nolint:errcheck
	for pid, stat := range map[string]string{"1": "1 (systemd) S 0 1 1 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 5 1000 300", "42": "42 (atlas (worker)) R 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 1000 900"} {
		os.MkdirAll(filepath.Join(root, pid), 0o700)                                                    //nolint:errcheck
		os.WriteFile(filepath.Join(root, pid, "stat"), []byte(stat), 0o600)                             //nolint:errcheck
		os.WriteFile(filepath.Join(root, pid, "cmdline"), []byte("atlas --db-password=hunter2"), 0o600) //nolint:errcheck
	}
	r := registry(t, Options{ProcRoot: root})
	res, err := invoke(t, r, "host.memory.status", "", nil)
	if err != nil || res.Data["used_percent"] != 75.0 {
		t.Fatalf("memory: %v %+v", err, res)
	}
	res, err = invoke(t, r, "host.process.list", "", map[string]any{"match": "atlas"})
	procs := res.Data["processes"].([]Process)
	if err != nil || len(procs) != 1 || procs[0].Name != "atlas (worker)" || procs[0].PID != 42 {
		t.Fatalf("processes: %v %+v", err, procs)
	}
	if strings.Contains(res.Summary+procs[0].Name, "hunter2") {
		t.Fatal("command line leaked")
	}
	if _, err := invoke(t, r, "host.disk.usage", "", map[string]any{"path": "relative"}); err == nil {
		t.Fatal("relative path accepted")
	}
	if res, err := invoke(t, r, "host.disk.usage", "", map[string]any{"path": root}); err != nil || res.Data["total_bytes"].(uint64) == 0 {
		t.Fatalf("disk: %v %+v", err, res)
	}
}

func TestScrub(t *testing.T) {
	out, n := Scrub("Authorization: Bearer eyJhbGci.x.y from 2001:db8:0:0:1:0:0:1 password=s3cret mail bob@example.co.uk")
	for _, leak := range []string{"eyJhbGci", "2001:db8", "s3cret", "bob@"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leak %q in %q", leak, out)
		}
	}
	if n != 4 {
		t.Fatalf("masked %d: %q", n, out)
	}
}
