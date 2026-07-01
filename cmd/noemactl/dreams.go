package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

func init() {
	extraCommands = append(extraCommands,
		command{"dreams", "<mind> list|run|promote <id> [note]|reject <id> [reason] – dreaming and its hypotheses", cmdDreams},
	)
}

func cmdDreams(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl dreams <mind> list | run | promote <id> [note] | reject <id> [reason]")
		return exitUsage
	}
	mind, base := args[0], "/api/v1/minds/"+args[0]+"/dreams"
	switch args[1] {
	case "list":
		var out struct {
			Items      []map[string]any
			Hypotheses []map[string]any
			Paused     bool
			Settings   map[string]any
		}
		if code, err := apiRequest("GET", base, nil, &out); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("mind %s · enabled=%v hours=%v interval=%v paused=%v\n", mind, out.Settings["enabled"], out.Settings["hours"], out.Settings["interval"], out.Paused)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "WHEN\tSTATUS\tSAMPLED\tLINKS\tHYPOTHESES\tERROR")
		for _, d := range out.Items {
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\t%v\n", d["created_at"], d["status"], d["sampled"], d["associations"], d["hypotheses"], d["error"])
		}
		tw.Flush()
		fmt.Println()
		tw = tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "STATUS\tCONF\tPROPOSITION\tID")
		for _, h := range out.Hypotheses {
			fmt.Fprintf(tw, "%v\t%.2f\t%v\t%v\n", h["status"], h["confidence"], h["proposition"], h["id"])
		}
		tw.Flush()
		return exitOK
	case "run":
		var d map[string]any
		if code, err := apiRequest("POST", base+"/run", nil, &d); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("%v: sampled=%v links=%v hypotheses=%v %v\n", d["status"], d["sampled"], d["associations"], d["hypotheses"], d["error"])
		return exitOK
	case "promote", "reject":
		if len(args) < 3 {
			return exitUsage
		}
		var h map[string]any
		if code, err := apiRequest("POST", base+"/hypotheses/"+args[2]+"/"+args[1], map[string]any{"note": strings.Join(args[3:], " ")}, &h); err != nil {
			return exitFor(code, err)
		}
		fmt.Printf("%v (%.2f)\n", h["status"], h["confidence"])
		return exitOK
	}
	return exitUsage
}
