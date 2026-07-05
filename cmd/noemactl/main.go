// Command noemactl administers a running Noema daemon over its HTTP API and
// performs a few offline operations (migrations, first user) directly against
// the database.
//
// Environment: NOEMA_URL (default http://127.0.0.1:8080), NOEMA_API_TOKEN for
// API commands; NOEMA_DATABASE_URL for offline commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/version"
	"github.com/andydixon/noema/migrations"
)

// Exit codes: 0 ok, 1 general failure, 2 usage error, 3 not found, 4 unauthorised.
const (
	exitOK = iota
	exitFail
	exitUsage
	exitNotFound
	exitUnauth
)

type command struct {
	name, help string
	run        func(args []string) int
}

var commands []command

// extraCommands are registered by other files in this package via init.
var extraCommands []command

func main() {
	commands = []command{
		{"status", "show daemon status", cmdStatus},
		{"migrate", "up|down|status – manage database schema (offline)", cmdMigrate},
		{"user", "create <username> [-role r] – manage users (offline)", cmdUser},
		{"version", "print version", func([]string) int { fmt.Println(version.Version); return exitOK }},
	}
	commands = append(commands, extraCommands...)
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		usage()
		os.Exit(exitUsage)
	}
	for _, c := range commands {
		if c.name == os.Args[1] {
			os.Exit(c.run(os.Args[2:]))
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
	usage()
	os.Exit(exitUsage)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: noemactl <command> [args]\n\ncommands:")
	for _, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", c.name, c.help)
	}
	fmt.Fprintln(os.Stderr, "\nenvironment: NOEMA_URL, NOEMA_API_TOKEN (API commands); NOEMA_DATABASE_URL (offline commands)")
}

// ---- API client ----

func apiRequest(method, path string, body any, out any) (int, error) {
	base := os.Getenv("NOEMA_URL")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, rd) // #nosec G704 -- NOEMA_URL is the operator\'s own server
	if err != nil {
		return 0, err
	}
	if tok := os.Getenv("NOEMA_API_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req) // #nosec G704 -- NOEMA_URL is the operator's own server
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 400 {
		var e struct {
			Error struct{ Code, Message string }
		}
		json.Unmarshal(data, &e) //nolint:errcheck
		return res.StatusCode, fmt.Errorf("%s: %s", res.Status, e.Error.Message)
	}
	if out != nil {
		return res.StatusCode, json.Unmarshal(data, out)
	}
	return res.StatusCode, nil
}

func exitFor(status int, err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	switch status {
	case http.StatusNotFound:
		return exitNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return exitUnauth
	}
	return exitFail
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "raw JSON output")
	if fs.Parse(args) != nil {
		return exitUsage
	}
	var st map[string]any
	if code, err := apiRequest("GET", "/api/v1/system/status", nil, &st); err != nil {
		return exitFor(code, err)
	}
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(st) //nolint:errcheck
		return exitOK
	}
	db, _ := st["database"].(map[string]any)
	fmt.Printf("version:    %v\nuptime:     %vs\ngoroutines: %v\ndatabase:   ok=%v pgvector=%v\n", st["version"], st["uptime_seconds"], st["goroutines"], db["ok"], db["pgvector"])
	return exitOK
}

// ---- offline commands ----

func openDB(ctx context.Context) (dbPool, int) {
	url := os.Getenv("NOEMA_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "NOEMA_DATABASE_URL is required for this command")
		return dbPool{}, exitUsage
	}
	pool, err := storage.Open(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return dbPool{}, exitFail
	}
	return dbPool{pool}, exitOK
}

func cmdMigrate(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl migrate up|down|status")
		return exitUsage
	}
	ctx := context.Background()
	db, code := openDB(ctx)
	if code != exitOK {
		return code
	}
	defer db.Close()
	switch args[0] {
	case "up":
		applied, err := storage.MigrateUp(ctx, db.Pool, migrations.FS)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitFail
		}
		if len(applied) == 0 {
			fmt.Println("database is up to date")
		}
		for _, a := range applied {
			fmt.Println("applied", a)
		}
	case "down":
		name, err := storage.MigrateDown(ctx, db.Pool, migrations.FS)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitFail
		}
		if name == "" {
			fmt.Println("nothing to roll back")
		} else {
			fmt.Println("rolled back", name)
		}
	case "status":
		st, err := storage.Status(ctx, db.Pool, migrations.FS)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitFail
		}
		for _, m := range st {
			state := "pending"
			if m.Applied {
				state = "applied " + m.AppliedAt
			}
			if m.Drift {
				state += " DRIFT"
			}
			fmt.Printf("%04d %-30s %s\n", m.Version, m.Name, state)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: noemactl migrate up|down|status")
		return exitUsage
	}
	return exitOK
}

func cmdUser(args []string) int {
	if len(args) < 2 || args[0] != "create" {
		fmt.Fprintln(os.Stderr, "usage: noemactl user create <username> [-role administrator|operator|observer]\n       password is read from NOEMA_PASSWORD or stdin")
		return exitUsage
	}
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	role := fs.String("role", "administrator", "role")
	if fs.Parse(args[2:]) != nil {
		return exitUsage
	}
	password := os.Getenv("NOEMA_PASSWORD")
	if password == "" {
		fmt.Fprint(os.Stderr, "password: ")
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1024))
		password = strings.TrimRight(string(b), "\r\n")
	}
	ctx := context.Background()
	db, code := openDB(ctx)
	if code != exitOK {
		return code
	}
	defer db.Close()
	svc := auth.NewService(db.Pool, time.Hour, time.Hour)
	u, err := svc.CreateUser(ctx, args[1], password, auth.Role(*role))
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			fmt.Fprintln(os.Stderr, "error: username already exists")
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return exitFail
	}
	db.Pool.Exec(ctx, `INSERT INTO audit_log (id, actor_kind, actor_name, action, target_kind, target_id, details) VALUES (gen_random_uuid(),'system','noemactl','user.create','user',$1,$2)`, //nolint:errcheck
		u.ID, fmt.Sprintf(`{"username":%q,"role":%q}`, u.Username, u.Role))
	fmt.Println("created user", u.Username, "with role", u.Role, "id", u.ID)
	return exitOK
}
