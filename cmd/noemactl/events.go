package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func init() {
	extraCommands = append(extraCommands,
		command{"event", "inspect <id>|list <mind> [-type t] [-n 50]|tail <mind> – cognitive events", cmdEvent},
		command{"observe", "<mind> <text> – inject an operator observation", cmdObserve},
	)
}

func printEvent(ev map[string]any) {
	fmt.Printf("%s  %-22s %-12s %s  [sal %.2f conf %.2f]\n", fmt.Sprint(ev["occurred_at"])[11:19], ev["type"], ev["source"], ev["summary"], num(ev["salience"]), num(ev["confidence"]))
}

func num(v any) float64 { f, _ := v.(float64); return f }

func cmdEvent(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl event inspect <id> | list <mind> [-type t] [-category c] [-n 50] [-json] | tail <mind>")
		return exitUsage
	}
	switch args[0] {
	case "inspect":
		var ctx map[string]any
		if code, err := apiRequest("GET", "/api/v1/events/"+args[1]+"/context", nil, &ctx); err != nil {
			return exitFor(code, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(ctx) //nolint:errcheck
		return exitOK
	case "list":
		fs := flag.NewFlagSet("event list", flag.ContinueOnError)
		typ := fs.String("type", "", "event type")
		cat := fs.String("category", "", "category")
		n := fs.Int("n", 50, "count")
		asJSON := fs.Bool("json", false, "raw JSON")
		if fs.Parse(args[2:]) != nil {
			return exitUsage
		}
		var out struct{ Items []map[string]any }
		q := fmt.Sprintf("?limit=%d&type=%s&category=%s", *n, *typ, *cat)
		if code, err := apiRequest("GET", "/api/v1/minds/"+args[1]+"/events"+q, nil, &out); err != nil {
			return exitFor(code, err)
		}
		if *asJSON {
			json.NewEncoder(os.Stdout).Encode(out.Items) //nolint:errcheck
			return exitOK
		}
		for i := len(out.Items) - 1; i >= 0; i-- {
			printEvent(out.Items[i])
		}
		return exitOK
	case "tail":
		base := os.Getenv("NOEMA_URL")
		if base == "" {
			base = "http://127.0.0.1:8080"
		}
		req, _ := http.NewRequest("GET", strings.TrimRight(base, "/")+"/api/v1/minds/"+args[1]+"/events/stream", nil) // #nosec G704 -- NOEMA_URL is the operator\'s own server
		req.Header.Set("Authorization", "Bearer "+os.Getenv("NOEMA_API_TOKEN"))
		res, err := (&http.Client{Timeout: 0}).Do(req) // #nosec G704 -- NOEMA_URL is the operator's own server
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitFail
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return exitFor(res.StatusCode, fmt.Errorf("%s", res.Status))
		}
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				var ev map[string]any
				if json.Unmarshal([]byte(line[6:]), &ev) == nil {
					printEvent(ev)
				}
			}
		}
		return exitOK
	}
	fmt.Fprintln(os.Stderr, "unknown event subcommand", args[0])
	return exitUsage
}

func cmdObserve(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl observe <mind> <text...>")
		return exitUsage
	}
	var ev map[string]any
	if code, err := apiRequest("POST", "/api/v1/minds/"+args[0]+"/observations", map[string]any{"text": strings.Join(args[1:], " "), "source": "noemactl"}, &ev); err != nil {
		return exitFor(code, err)
	}
	fmt.Printf("recorded observation %v (seq %v) at %v\n", ev["id"], ev["seq"], time.Now().Format(time.RFC3339))
	return exitOK
}
