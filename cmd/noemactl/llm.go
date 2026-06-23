package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

func init() {
	extraCommands = append(extraCommands,
		command{"provider", "list|test <provider-id> [model] – LLM providers", cmdProvider},
		command{"prompt", "list|show <name> – prompt templates", cmdPrompt},
	)
}

func cmdProvider(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl provider list | test <provider-id> [model]")
		return exitUsage
	}
	switch args[0] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/llm/providers", nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tKIND\tENDPOINT\tKEY\tENABLED\tID")
		for _, p := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\t%v\n", p["name"], p["kind"], p["endpoint"], p["api_key"], p["enabled"], p["id"])
		}
		tw.Flush()
		return exitOK
	case "test":
		if len(args) < 2 {
			return exitUsage
		}
		body := map[string]any{}
		if len(args) > 2 {
			body["model"] = args[2]
		}
		var res map[string]any
		if code, err := apiRequest("POST", "/api/v1/llm/providers/"+args[1]+"/test", body, &res); err != nil {
			return exitFor(code, err)
		}
		if ok, _ := res["ok"].(bool); !ok {
			fmt.Println("test failed:", res["error"])
			return exitFail
		}
		fmt.Printf("ok: %v answered %q in %v ms\n", res["model"], res["text"], res["latency_ms"])
		return exitOK
	}
	return exitUsage
}

func cmdPrompt(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl prompt list | show <name>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/prompts", nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tROLE\tVERSION\tDESCRIPTION")
		for _, p := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\tv%v\t%v\n", p["name"], p["role"], p["current_version"], p["description"])
		}
		tw.Flush()
		return exitOK
	case "show":
		if len(args) < 2 {
			return exitUsage
		}
		var p map[string]any
		if code, err := apiRequest("GET", "/api/v1/prompts/"+args[1], nil, &p); err != nil {
			return exitFor(code, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(p) //nolint:errcheck
		return exitOK
	}
	return exitUsage
}
