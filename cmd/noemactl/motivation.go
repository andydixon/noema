package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func init() {
	extraCommands = append(extraCommands,
		command{"goal", "list <mind>|add <mind> <title...> – goals", cmdGoal},
		command{"prediction", "list <mind>|calibration <mind> – predictions", cmdPrediction},
		command{"state", "show <mind>|reset <mind> – drives and affect", cmdState},
	)
}

func cmdGoal(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl goal list <mind> | add <mind> <title...>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/minds/"+args[1]+"/goals", nil, &out); err != nil {
			return exitFor(code, err)
		}
		for _, g := range out.Items {
			fmt.Printf("%-10v %.2f %3.0f%%  %s  [%v]\n", g["status"], num(g["priority"]), num(g["progress"])*100, g["title"], g["kind"])
		}
		return exitOK
	case "add":
		var g map[string]any
		if code, err := apiRequest("POST", "/api/v1/minds/"+args[1]+"/goals", map[string]any{"title": strings.Join(args[2:], " "), "kind": "task", "priority": 0.6}, &g); err != nil {
			return exitFor(code, err)
		}
		fmt.Println("created goal", g["id"])
		return exitOK
	}
	return exitUsage
}

func cmdPrediction(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl prediction list <mind> | calibration <mind>")
		return exitUsage
	}
	switch args[0] {
	case "list":
		var out struct{ Items []map[string]any }
		if code, err := apiRequest("GET", "/api/v1/minds/"+args[1]+"/predictions", nil, &out); err != nil {
			return exitFor(code, err)
		}
		for _, p := range out.Items {
			fmt.Printf("%-10v %.2f  %s  (until %v)\n", p["status"], num(p["confidence"]), p["statement"], fmt.Sprint(p["window_end"])[:16])
		}
		return exitOK
	case "calibration":
		var c map[string]any
		if code, err := apiRequest("GET", "/api/v1/minds/"+args[1]+"/predictions/calibration", nil, &c); err != nil {
			return exitFor(code, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(c) //nolint:errcheck
		return exitOK
	}
	return exitUsage
}

func cmdState(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl state show <mind> | reset <mind>")
		return exitUsage
	}
	var out map[string]any
	switch args[0] {
	case "show":
		if code, err := apiRequest("GET", "/api/v1/minds/"+args[1]+"/state", nil, &out); err != nil {
			return exitFor(code, err)
		}
	case "reset":
		if code, err := apiRequest("POST", "/api/v1/minds/"+args[1]+"/state/reset", map[string]any{}, &out); err != nil {
			return exitFor(code, err)
		}
		fmt.Println("reset to baseline")
		return exitOK
	default:
		return exitUsage
	}
	st, _ := out["state"].(map[string]any)
	if aff, ok := st["affect"].(map[string]any); ok {
		fmt.Println("affect:")
		for _, k := range []string{"valence", "arousal", "curiosity", "concern", "frustration", "confidence", "uncertainty", "engagement", "task_pressure", "load"} {
			fmt.Printf("  %-14s %5.2f\n", k, num(aff[k]))
		}
	}
	if drives, ok := st["drives"].([]any); ok {
		fmt.Println("drives:")
		for _, d := range drives {
			dm, _ := d.(map[string]any)
			fmt.Printf("  %-22s %5.2f  [%.2f–%.2f] baseline %.2f\n", dm["name"], num(dm["value"]), num(dm["min"]), num(dm["max"]), num(dm["baseline"]))
		}
	}
	return exitOK
}
