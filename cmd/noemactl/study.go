package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

func init() {
	extraCommands = append(extraCommands,
		command{"snapshots", "<mind> list|create [name]|fork <snapshot-id> <slug>|export|import <file> <slug> – snapshots, forks, archives", cmdSnapshots},
		command{"compare", "<mind-a> <mind-b> – deterministic comparison of two minds", cmdCompare},
		command{"replay", "<mind> list|start <snapshot-id> [from] [to]|show <replay-id> – cognitive replay", cmdReplay},
		command{"experiments", "list|create <file.json>|show <id>|run <id>|export <id> – cognitive experiments", cmdExperiments},
	)
}

func cmdSnapshots(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl snapshots <mind> list | create [name] | fork <snapshot-id> <slug> | export | import <file> <slug>")
		return exitUsage
	}
	mind := args[0]
	switch args[1] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/minds/"+mind+"/snapshots", nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tEVENT\tSIZE\tCREATED\tID")
		for _, s := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\n", s["name"], s["event_seq"], s["size_bytes"], s["created_at"], s["id"])
		}
		tw.Flush()
		return exitOK
	case "create":
		var sn map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/"+mind+"/snapshots", map[string]any{"name": strings.Join(args[2:], " ")}, &sn); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("snapshot %v at event %v\n", sn["id"], sn["event_seq"])
		return exitOK
	case "fork":
		if len(args) < 4 {
			return exitUsage
		}
		var m map[string]any
		if code, err := apiRequest("POST", "/api/v1/snapshots/"+args[2]+"/fork", map[string]any{"slug": args[3]}, &m); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("forked into %v (%v)\n", m["slug"], m["id"])
		return exitOK
	case "export":
		var a json.RawMessage
		if code, err := apiRequest("GET", "/api/v1/minds/"+mind+"/export", nil, &a); err != nil {
			return exitFor(code, err)
		}
		os.Stdout.Write(a) //nolint:errcheck
		fmt.Println()
		return exitOK
	case "import":
		if len(args) < 4 {
			return exitUsage
		}
		raw, err := os.ReadFile(args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitUsage
		}
		var m map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/import-archive", map[string]any{"slug": args[3], "archive": json.RawMessage(raw)}, &m); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("imported as %v (%v)\n", m["slug"], m["id"])
		return exitOK
	}
	return exitUsage
}

func cmdCompare(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl compare <mind-a> <mind-b>")
		return exitUsage
	}
	var out struct {
		A, B       string
		Comparison struct {
			Stats   []map[string]any
			Beliefs []map[string]any
			Config  []map[string]any
			Summary map[string]int
		}
	}
	if code, err := apiRequest("GET", "/api/v1/minds/"+args[0]+"/compare/"+args[1], nil, &out); err != nil {
		return exitFor(code, err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "STAT\t%s\t%s\n", out.A, out.B)
	for _, s := range out.Comparison.Stats {
		fmt.Fprintf(tw, "%v\t%v\t%v\n", s["name"], s["a"], s["b"])
	}
	for _, c := range out.Comparison.Config {
		fmt.Fprintf(tw, "config.%v\t%v\t%v\n", c["field"], c["a"], c["b"])
	}
	tw.Flush()
	fmt.Printf("beliefs: %d same, %d changed, %d only in %s, %d only in %s\n", out.Comparison.Summary["beliefs_same"], out.Comparison.Summary["beliefs_changed"], out.Comparison.Summary["beliefs_only_a"], out.A, out.Comparison.Summary["beliefs_only_b"], out.B)
	for _, b := range out.Comparison.Beliefs {
		fmt.Printf("  %v  a=%v b=%v\n", b["proposition"], b["conf_a"], b["conf_b"])
	}
	return exitOK
}

func cmdReplay(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl replay <mind> list | start <snapshot-id> [from] [to] | show <replay-id>")
		return exitUsage
	}
	mind := args[0]
	switch args[1] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/minds/"+mind+"/replays", nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "STATUS\tRANGE\tSTEPS\tEXACT\tAPPROX\tDIVERGED\tID")
		for _, r := range out.Items {
			s, _ := r["summary"].(map[string]any)
			fmt.Fprintf(tw, "%v\t%v-%v\t%v\t%v\t%v\t%v\t%v\n", r["status"], r["from_seq"], r["to_seq"], s["steps"], s["exact"], s["approximate"], s["diverged"], r["id"])
		}
		tw.Flush()
		return exitOK
	case "start":
		if len(args) < 3 {
			return exitUsage
		}
		body := map[string]any{"snapshot_id": args[2]}
		if len(args) > 3 {
			body["from_seq"], _ = strconv.ParseInt(args[3], 10, 64)
		}
		if len(args) > 4 {
			body["to_seq"], _ = strconv.ParseInt(args[4], 10, 64)
		}
		var r map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/"+mind+"/replays", body, &r); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("replay %v started (events %v-%v); watch with: noemactl replay %s show %v\n", r["id"], r["from_seq"], r["to_seq"], mind, r["id"])
		return exitOK
	case "show":
		if len(args) < 3 {
			return exitUsage
		}
		var r struct {
			Status  string
			Error   string
			Summary map[string]any
			Steps   []map[string]any
		}
		if code, err := apiRequest("GET", "/api/v1/replays/"+args[2], nil, &r); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("%s %v %s\n", r.Status, r.Summary, r.Error)
		for _, st := range r.Steps {
			fmt.Printf("[%v] %v — %v %v\n", st["label"], st["summary"], st["note"], "")
			fmt.Printf("    recorded: %v\n    replayed: %v\n", st["original"], st["replayed"])
		}
		return exitOK
	}
	return exitUsage
}

func cmdExperiments(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl experiments list | create <file.json> | show <id> | run <id> | export <id>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/experiments", nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "STATUS\tNAME\tID")
		for _, e := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\t%v\n", e["status"], e["name"], e["id"])
		}
		tw.Flush()
		return exitOK
	case "create":
		if len(args) < 2 {
			return exitUsage
		}
		var raw []byte
		var err error
		if args[1] == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(args[1])
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitUsage
		}
		var e map[string]any
		if code, err := apiRequest("POST", "/api/v1/experiments", json.RawMessage(raw), &e); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("created %v\n", e["id"])
		return exitOK
	case "show", "export":
		if len(args) < 2 {
			return exitUsage
		}
		path := "/api/v1/experiments/" + args[1]
		if args[0] == "export" {
			path += "/export"
		}
		var raw json.RawMessage
		if code, err := apiRequest("GET", path, nil, &raw); err != nil {
			return exitFor(code, err)
		}
		os.Stdout.Write(raw) //nolint:errcheck
		fmt.Println()
		return exitOK
	case "run":
		if len(args) < 2 {
			return exitUsage
		}
		var e map[string]any
		if code, err := apiRequest("POST", "/api/v1/experiments/"+args[1]+"/run", nil, &e); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("%v: %v\n", e["name"], e["status"])
		return exitOK
	}
	return exitUsage
}
