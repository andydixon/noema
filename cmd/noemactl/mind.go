package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
)

func init() {
	extraCommands = append(extraCommands,
		command{"mind", "list|create|inspect|clone|personality – manage minds", cmdMind},
		command{"template", "list – personality templates", cmdTemplate},
	)
}

func cmdMind(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl mind list|create <slug>|inspect <mind>|clone <mind> <new-slug>|personality <mind>|import <seed.json>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("mind list", flag.ContinueOnError)
		all := fs.Bool("all", false, "include archived")
		asJSON := fs.Bool("json", false, "raw JSON")
		if fs.Parse(args[1:]) != nil {
			return exitUsage
		}
		var out struct{ Items []map[string]any }
		q := ""
		if *all {
			q = "?include_archived=true"
		}
		if code, err := apiRequest("GET", "/api/v1/minds"+q, nil, &out); err != nil {
			return exitFor(code, err)
		}
		if *asJSON {
			json.NewEncoder(os.Stdout).Encode(out.Items) //nolint:errcheck
			return exitOK
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SLUG\tNAME\tSTATUS\tPERSONALITY\tID")
		for _, m := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\t%v\tv%v\t%v\n", m["slug"], m["display_name"], m["status"], m["personality_version"], m["id"])
		}
		tw.Flush()
		return exitOK
	case "create":
		fs := flag.NewFlagSet("mind create", flag.ContinueOnError)
		name := fs.String("name", "", "display name")
		if len(args) < 2 || fs.Parse(args[2:]) != nil {
			fmt.Fprintln(os.Stderr, "usage: noemactl mind create <slug> [-name display]")
			return exitUsage
		}
		var m map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds", map[string]any{"slug": args[1], "display_name": *name}, &m); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("created mind %v (%v)\n", m["slug"], m["id"])
		return exitOK
	case "inspect":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: noemactl mind inspect <mind>")
			return exitUsage
		}
		var m map[string]any
		if code, err := apiRequest("GET", "/api/v1/minds/"+args[1], nil, &m); err != nil {
			return exitFor(code, err)
		}
		var sm map[string]any
		apiRequest("GET", "/api/v1/minds/"+args[1]+"/selfmodel", nil, &sm) //nolint:errcheck
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"mind": m, "self_model": sm}) //nolint:errcheck
		return exitOK
	case "clone":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: noemactl mind clone <mind> <new-slug>")
			return exitUsage
		}
		var m map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/"+args[1]+"/clone", map[string]any{"slug": args[2]}, &m); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("cloned into %v (%v)\n", m["slug"], m["id"])
		return exitOK
	case "import":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: noemactl mind import <seed.json>")
			return exitUsage
		}
		raw, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitFail
		}
		var seed map[string]any
		if err := json.Unmarshal(raw, &seed); err != nil {
			fmt.Fprintln(os.Stderr, "error: not valid JSON:", err)
			return exitFail
		}
		var res map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/import", seed, &res); err != nil {
			return exitFor(code, err)
		}
		m, _ := res["mind"].(map[string]any)
		fmt.Printf("imported mind %v (%v): %v memories, %v beliefs\n", m["slug"], m["id"], res["memories"], res["beliefs"])
		if p, ok := res["problems"].([]any); ok && len(p) > 0 {
			fmt.Println("problems:", p)
		}
		return exitOK
	case "personality":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: noemactl mind personality <mind>")
			return exitUsage
		}
		var b map[string]string
		if code, err := apiRequest("GET", "/api/v1/minds/"+args[1]+"/personality/brief", nil, &b); err != nil {
			return exitFor(code, err)
		}
		fmt.Print(b["brief"])
		return exitOK
	}
	fmt.Fprintln(os.Stderr, "unknown mind subcommand", args[0])
	return exitUsage
}

func cmdTemplate(args []string) int {
	var out struct{ Items []map[string]any }
	if code, err := apiRequest("GET", "/api/v1/personality-templates", nil, &out); err != nil {
		return exitFor(code, err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDESCRIPTION\tID")
	for _, t := range out.Items {
		fmt.Fprintf(tw, "%v\t%v\t%v\n", t["name"], t["description"], t["id"])
	}
	tw.Flush()
	return exitOK
}
