package main

import (
	"fmt"
	"os"
)

func init() {
	extraCommands = append(extraCommands,
		command{"narrative", "<mind> – current internal narrative", cmdNarrative},
		command{"consolidate", "<mind> – run consolidation now", cmdConsolidate},
		command{"reflect", "<mind> – run reflection now", cmdReflect},
		command{"notifications", "list [-mind m]|read-all – notifications", cmdNotifications},
	)
}

func cmdNarrative(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl narrative <mind>")
		return exitUsage
	}
	var n map[string]any
	if code, err := apiRequest("GET", "/api/v1/minds/"+args[0]+"/narrative", nil, &n); err != nil {
		return exitFor(code, err)
	}
	fmt.Println(n["text"])
	return exitOK
}

func cmdConsolidate(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl consolidate <mind>")
		return exitUsage
	}
	var j map[string]any
	if code, err := apiRequest("POST", "/api/v1/minds/"+args[0]+"/consolidation/run", map[string]any{}, &j); err != nil {
		return exitFor(code, err)
	}
	fmt.Printf("consolidation %v: %v cluster(s), %v generalisation(s)\n", j["status"], j["clusters"], j["produced"])
	if outs, ok := j["outputs"].([]any); ok {
		for _, o := range outs {
			om, _ := o.(map[string]any)
			fmt.Printf("  %.2f  %v  (%v episodes, %v)\n", num(om["confidence"]), om["proposition"], len(om["episodes"].([]any)), om["wording"])
		}
	}
	return exitOK
}

func cmdReflect(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl reflect <mind>")
		return exitUsage
	}
	var r map[string]any
	if code, err := apiRequest("POST", "/api/v1/minds/"+args[0]+"/reflections/run", map[string]any{}, &r); err != nil {
		return exitFor(code, err)
	}
	fmt.Println(r["prose"])
	return exitOK
}

func cmdNotifications(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: noemactl notifications list [-mind m] | read-all")
		return exitUsage
	}
	switch args[0] {
	case "list":
		path := "/api/v1/notifications"
		if len(args) >= 3 && args[1] == "-mind" {
			path = "/api/v1/minds/" + args[2] + "/notifications"
		}
		var out struct {
			Items  []map[string]any
			Unread int
		}
		if code, err := apiRequest("GET", path, nil, &out); err != nil {
			return exitFor(code, err)
		}
		for _, n := range out.Items {
			fmt.Printf("%-6v %-5v ×%v  %v — %v  [%v]\n", n["level"], n["status"], n["count"], n["title"], n["body"], n["rule"])
		}
		fmt.Printf("(%d unread)\n", out.Unread)
		return exitOK
	case "read-all":
		var res map[string]any
		if code, err := apiRequest("POST", "/api/v1/notifications/read-all", map[string]any{}, &res); err != nil {
			return exitFor(code, err)
		}
		fmt.Println("marked", res["marked"])
		return exitOK
	}
	return exitUsage
}
