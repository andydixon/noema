package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func init() {
	extraCommands = append(extraCommands, command{"belief", "list --mind m [-status s]|inspect <mind> <id>|assert <mind> <text>|wrong <mind> <id> <reason> – beliefs", cmdBelief})
}

func cmdBelief(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl belief list --mind <mind> [-status s] [-json] | inspect <mind> <id> | assert <mind> <text...> | wrong <mind> <id> <reason...>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("belief list", flag.ContinueOnError)
		mind := fs.String("mind", "", "mind slug or id")
		status := fs.String("status", "", "status filter")
		asJSON := fs.Bool("json", false, "raw JSON")
		if fs.Parse(args[1:]) != nil || *mind == "" {
			fmt.Fprintln(os.Stderr, "usage: noemactl belief list --mind <mind> [-status s]")
			return exitUsage
		}
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/minds/"+*mind+"/beliefs?status="+*status, nil, &out); err != nil {
			return exitFor(code, err)
		}
		if *asJSON {
			json.NewEncoder(os.Stdout).Encode(out.Items) //nolint:errcheck
			return exitOK
		}
		for _, b := range out.Items {
			fmt.Printf("%.2f  %-10v %s  (%v)\n", num(b["confidence"]), b["status"], b["proposition"], b["id"])
		}
		return exitOK
	case "inspect":
		if len(args) < 3 {
			return exitUsage
		}
		var d map[string]any
		if code, err := apiRequest("GET", "/api/v1/minds/"+args[1]+"/beliefs/"+args[2], nil, &d); err != nil {
			return exitFor(code, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(d) //nolint:errcheck
		return exitOK
	case "assert":
		if len(args) < 3 {
			return exitUsage
		}
		var res map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/"+args[1]+"/beliefs", map[string]any{"proposition": strings.Join(args[2:], " ")}, &res); err != nil {
			return exitFor(code, err)
		}
		b, _ := res["Belief"].(map[string]any)
		fmt.Printf("recorded %v belief %v with confidence %.2f\n", b["status"], b["id"], num(b["confidence"]))
		return exitOK
	case "wrong":
		if len(args) < 4 {
			return exitUsage
		}
		var res map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/"+args[1]+"/beliefs/"+args[2]+"/retract", map[string]any{"reason": strings.Join(args[3:], " ")}, &res); err != nil {
			return exitFor(code, err)
		}
		fmt.Println("belief rejected; history preserved")
		return exitOK
	}
	fmt.Fprintln(os.Stderr, "unknown belief subcommand", args[0])
	return exitUsage
}
