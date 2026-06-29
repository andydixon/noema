package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

func init() {
	extraCommands = append(extraCommands,
		command{"approvals", "list|approve <id> [note]|session <id>|deny <id> [note] – approval centre", cmdApprovals},
		command{"policy", "list|add -mind m -capability c -target t -effect e [-reason r]|delete <id> – capability policies", cmdPolicy},
		command{"invoke", "<mind> <capability> [target] [json-params] – invoke a capability through policy", cmdInvoke},
	)
}

func cmdApprovals(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl approvals list | approve <id> [note] | session <id> [note] | deny <id> [note]")
		return exitUsage
	}
	switch args[0] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/approvals?status=pending", nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RISK\tCAPABILITY\tTARGET\tREASON\tID")
		for _, a := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\n", a["risk"], a["capability"], a["target"], a["reason"], a["id"])
		}
		tw.Flush()
		return exitOK
	case "approve", "session", "deny":
		if len(args) < 2 {
			return exitUsage
		}
		note := strings.Join(args[2:], " ")
		path := "/api/v1/approvals/" + args[1] + "/approve"
		body := map[string]any{"note": note, "for_session": args[0] == "session"}
		if args[0] == "deny" {
			path = "/api/v1/approvals/" + args[1] + "/deny"
			body = map[string]any{"note": note}
		}
		var res map[string]any
		if code, err := apiRequest("POST", path, body, &res); err != nil {
			return exitFor(code, err)
		}
		if st, ok := res["status"]; ok {
			fmt.Println("result:", st)
			if r, ok := res["result"].(map[string]any); ok {
				fmt.Println(r["summary"])
			}
		} else {
			fmt.Println("recorded")
		}
		return exitOK
	}
	return exitUsage
}

func cmdPolicy(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl policy list | add -mind m -capability c -target t -effect allow|ask|deny [-reason r] | delete <id>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/policies", nil, &out); err != nil {
			return exitFor(code, err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SCOPE\tCAPABILITY\tTARGET\tEFFECT\tREASON\tID")
		for _, p := range out.Items {
			scope := "global"
			if m, _ := p["mind_id"].(string); m != "" {
				scope = m[:8]
			}
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\t%v\n", scope, p["capability"], p["target_pattern"], p["effect"], p["reason"], p["id"])
		}
		tw.Flush()
		return exitOK
	case "add":
		body := map[string]any{"target_pattern": "*"}
		for i := 1; i+1 < len(args); i += 2 {
			switch args[i] {
			case "-mind":
				var m map[string]any
				if code, err := apiRequest("GET", "/api/v1/minds/"+args[i+1], nil, &m); err != nil {
					return exitFor(code, err)
				}
				body["mind_id"] = m["id"]
			case "-capability":
				body["capability"] = args[i+1]
			case "-target":
				body["target_pattern"] = args[i+1]
			case "-effect":
				body["effect"] = args[i+1]
			case "-reason":
				body["reason"] = args[i+1]
			}
		}
		var p map[string]any
		if code, err := apiRequest("POST", "/api/v1/policies", body, &p); err != nil {
			return exitFor(code, err)
		}
		fmt.Println("policy", p["id"], "added")
		return exitOK
	case "delete":
		if len(args) < 2 {
			return exitUsage
		}
		if code, err := apiRequest("DELETE", "/api/v1/policies/"+args[1], nil, nil); err != nil {
			return exitFor(code, err)
		}
		fmt.Println("deleted")
		return exitOK
	}
	return exitUsage
}

func cmdInvoke(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl invoke <mind> <capability> [target] [json-params]")
		return exitUsage
	}
	body := map[string]any{"capability": args[1]}
	if len(args) > 2 {
		body["target"] = args[2]
	}
	if len(args) > 3 {
		var params map[string]any
		if err := json.Unmarshal([]byte(args[3]), &params); err != nil {
			fmt.Fprintln(os.Stderr, "params must be a JSON object")
			return exitUsage
		}
		body["params"] = params
	}
	var out map[string]any
	if code, err := apiRequest("POST", "/api/v1/minds/"+args[0]+"/invoke", body, &out); err != nil {
		return exitFor(code, err)
	}
	d, _ := out["decision"].(map[string]any)
	fmt.Printf("%v (%v: %v)\n", out["status"], d["effect"], d["reason"])
	if out["approval_id"] != nil && out["approval_id"] != "" {
		fmt.Println("approval:", out["approval_id"])
	}
	if r, ok := out["result"].(map[string]any); ok {
		fmt.Println(r["summary"])
	}
	return exitOK
}
