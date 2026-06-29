package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
)

func init() {
	extraCommands = append(extraCommands, command{"entity", "list <mind> [query]|inspect <mind> <id>|merge <mind> <id> <other>|distinct <mind> <id> <other> – relationship graph", cmdEntity})
}

func cmdEntity(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl entity list <mind> [query] | inspect <mind> <id> | merge <mind> <id> <other> | distinct <mind> <id> <other>")
		return exitUsage
	}
	mind := args[1]
	switch args[0] {
	case "list":
		q := ""
		if len(args) > 2 {
			q = "?q=" + url.QueryEscape(strings.Join(args[2:], " "))
		}
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/minds/"+mind+"/entities"+q, nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tNAME\tMENTIONS\tID")
		for _, e := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\n", e["kind"], e["name"], e["mention_count"], e["id"])
		}
		tw.Flush()
		return exitOK
	case "inspect":
		if len(args) < 3 {
			return exitUsage
		}
		var d map[string]any
		if code, err := apiRequest("GET", "/api/v1/minds/"+mind+"/entities/"+args[2], nil, &d); err != nil {
			return exitFor(code, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(d) //nolint:errcheck
		return exitOK
	case "merge", "distinct":
		if len(args) < 4 {
			return exitUsage
		}
		var d map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/"+mind+"/entities/"+args[2]+"/"+args[0], map[string]any{"other": strings.Join(args[3:], " ")}, &d); err != nil {
			return exitFor(code, err)
		}
		fmt.Println(args[0], "recorded")
		return exitOK
	}
	return exitUsage
}
