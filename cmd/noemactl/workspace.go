package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func init() {
	extraCommands = append(extraCommands, command{"workspace", "show <mind>|cycle <mind>|pause <mind>|resume <mind>|reset <mind> – workspace and cognition control", cmdWorkspace})
}

func cmdWorkspace(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl workspace show|cycle|pause|resume|reset <mind>")
		return exitUsage
	}
	mind := args[1]
	var v struct {
		Running bool `json:"running"`
		State   struct {
			Cycle         int64            `json:"cycle"`
			Mode          string           `json:"mode"`
			Workspace     []map[string]any `json:"workspace"`
			WorkingMemory []map[string]any `json:"working_memory"`
		} `json:"state"`
		Candidates []map[string]any `json:"last_candidates"`
	}
	var code int
	var err error
	switch args[0] {
	case "show":
		code, err = apiRequest("GET", "/api/v1/minds/"+mind+"/workspace", nil, &v)
	case "cycle":
		code, err = apiRequest("POST", "/api/v1/minds/"+mind+"/cycle", map[string]any{}, &v)
	case "pause":
		code, err = apiRequest("POST", "/api/v1/minds/"+mind+"/mode", map[string]any{"mode": "paused"}, &v)
	case "resume":
		code, err = apiRequest("POST", "/api/v1/minds/"+mind+"/mode", map[string]any{"mode": "awake"}, &v)
	case "reset":
		code, err = apiRequest("POST", "/api/v1/minds/"+mind+"/reset", map[string]any{"workspace": true, "working_memory": true, "loops": true}, &v)
	default:
		fmt.Fprintln(os.Stderr, "unknown workspace subcommand", args[0])
		return exitUsage
	}
	if err != nil {
		return exitFor(code, err)
	}
	if len(args) > 2 && args[2] == "-json" {
		json.NewEncoder(os.Stdout).Encode(v) //nolint:errcheck
		return exitOK
	}
	fmt.Printf("mode: %s  cycle: %d  running: %v\n\nworkspace (%d):\n", v.State.Mode, v.State.Cycle, v.Running, len(v.State.Workspace))
	for i, t := range v.State.Workspace {
		fmt.Printf("  %d. [%.2f ×%.2f] %s  (%s/%s)\n", i+1, num(t["score"]), num(t["activation"]), t["summary"], t["process"], t["kind"])
	}
	fmt.Printf("\nworking memory (%d):\n", len(v.State.WorkingMemory))
	for _, it := range v.State.WorkingMemory {
		fmt.Printf("  [%.2f] %s (%s)\n", num(it["activation"]), it["content"], it["kind"])
	}
	if len(v.Candidates) > 0 {
		fmt.Printf("\nlast cycle candidates (%d):\n", len(v.Candidates))
		for _, c := range v.Candidates {
			md, _ := c["metadata"].(map[string]any)
			fmt.Printf("  %-10v %.2f %s\n", md["fate"], num(c["salience"]), c["summary"])
		}
	}
	return exitOK
}
