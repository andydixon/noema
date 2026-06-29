package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

func init() {
	extraCommands = append(extraCommands,
		command{"integrations", "list|add <mind> <kind> <name> <target> [secret]|run <id>|runs <id>|delete <id> – external sources", cmdIntegrations},
	)
}

func cmdIntegrations(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl integrations list | add <mind> research_folder|git|prometheus|webhook <name> <path|url> [secret] | run <id> | runs <id> | delete <id>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/integrations", nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tKIND\tSTATUS\tINGESTED\tID")
		for _, i := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\n", i["name"], i["kind"], i["status"], i["ingested_count"], i["id"])
		}
		tw.Flush()
		return exitOK
	case "add":
		if len(args) < 5 {
			return exitUsage
		}
		cfg := map[string]any{}
		switch args[2] {
		case "research_folder":
			cfg["path"] = args[4]
		case "git":
			cfg["url"] = args[4]
		case "prometheus":
			cfg["endpoint"] = args[4]
		}
		body := map[string]any{"mind": args[1], "kind": args[2], "name": args[3], "config": cfg, "enabled": true}
		if len(args) > 5 {
			body["secret"] = strings.Join(args[5:], " ")
		}
		var res map[string]any
		if code, err := apiRequest("POST", "/api/v1/integrations", body, &res); err != nil {
			return exitFor(code, err)
		}
		fmt.Println("created", res["id"])
		return exitOK
	case "run":
		if len(args) < 2 {
			return exitUsage
		}
		var res map[string]any
		if code, err := apiRequest("POST", "/api/v1/integrations/"+args[1]+"/run", nil, &res); err != nil {
			return exitFor(code, err)
		}
		run, _ := res["run"].(map[string]any)
		fmt.Printf("items=%v observations=%v errors=%v %v\n", run["items"], run["observations"], run["errors"], run["error"])
		return exitOK
	case "runs":
		if len(args) < 2 {
			return exitUsage
		}
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/integrations/"+args[1]+"/runs", nil, &out); err != nil {
			return exitFor(code, err)
		}
		for _, r := range out.Items {
			fmt.Printf("%v items=%v obs=%v errors=%v %v\n", r["started_at"], r["items"], r["observations"], r["errors"], r["error"])
		}
		return exitOK
	case "delete":
		if len(args) < 2 {
			return exitUsage
		}
		if code, err := apiRequest("DELETE", "/api/v1/integrations/"+args[1], nil, nil); err != nil {
			return exitFor(code, err)
		}
		fmt.Println("deleted")
		return exitOK
	}
	return exitUsage
}
