package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

func init() {
	extraCommands = append(extraCommands, command{"memory", "search <mind> <query>|list <mind>|inspect <mind> <id>|add <mind> [-kind k] [-epistemic e] <text> – long-term memory", cmdMemory})
}

func cmdMemory(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl memory search <mind> <query...> | list <mind> [-kind k] | inspect <mind> <id> | add <mind> [-kind semantic] [-epistemic fact] [-confidence 0.8] <text...>")
		return exitUsage
	}
	mind := args[1]
	switch args[0] {
	case "search":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/minds/"+mind+"/memories?q="+url.QueryEscape(strings.Join(args[2:], " ")), nil, &out); err != nil {
			return exitFor(code, err)
		}
		for _, h := range out.Items {
			m, _ := h["memory"].(map[string]any)
			fmt.Printf("%.2f  [%s/%s] %s  (%s)\n", num(h["score"]), m["kind"], m["epistemic"], m["content"], m["id"])
			if comps, ok := h["components"].(map[string]any); ok {
				var parts []string
				for k, v := range comps {
					parts = append(parts, fmt.Sprintf("%s=%.2f", k, num(v)))
				}
				fmt.Printf("      %s\n", strings.Join(parts, " "))
			}
		}
		return exitOK
	case "list":
		fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
		kind := fs.String("kind", "", "kind filter")
		n := fs.Int("n", 50, "count")
		if fs.Parse(args[2:]) != nil {
			return exitUsage
		}
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", fmt.Sprintf("/api/v1/minds/%s/memories?kind=%s&limit=%d", mind, *kind, *n), nil, &out); err != nil {
			return exitFor(code, err)
		}
		for _, m := range out.Items {
			fmt.Printf("%s  [%s/%s conf %.2f imp %.2f act %.2f] %s  (%s)\n", fmt.Sprint(m["occurred_at"])[:16], m["kind"], m["epistemic"], num(m["confidence"]), num(m["importance"]), num(m["activation"]), m["content"], m["id"])
		}
		return exitOK
	case "inspect":
		if len(args) < 3 {
			return exitUsage
		}
		var d map[string]any
		if code, err := apiRequest("GET", "/api/v1/minds/"+mind+"/memories/"+args[2], nil, &d); err != nil {
			return exitFor(code, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(d) //nolint:errcheck
		return exitOK
	case "add":
		fs := flag.NewFlagSet("memory add", flag.ContinueOnError)
		kind := fs.String("kind", "semantic", "memory kind")
		epi := fs.String("epistemic", "fact", "epistemic kind")
		conf := fs.Float64("confidence", 0.8, "confidence")
		if fs.Parse(args[2:]) != nil || fs.NArg() == 0 {
			fmt.Fprintln(os.Stderr, "usage: noemactl memory add <mind> [-kind k] [-epistemic e] [-confidence c] <text...>")
			return exitUsage
		}
		var m map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/"+mind+"/memories", map[string]any{"kind": *kind, "epistemic": *epi, "confidence": *conf, "content": strings.Join(fs.Args(), " ")}, &m); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("stored %v memory %v\n", m["kind"], m["id"])
		return exitOK
	}
	fmt.Fprintln(os.Stderr, "unknown memory subcommand", args[0])
	return exitUsage
}
