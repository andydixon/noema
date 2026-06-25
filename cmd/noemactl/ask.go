package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func init() {
	extraCommands = append(extraCommands, command{"ask", "<mind> <text...> [-wait 30] – talk to a mind and wait for its reply", cmdAsk})
}

func cmdAsk(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noemactl ask <mind> <text...> [-wait seconds]")
		return exitUsage
	}
	wait := 30
	var words []string
	for i := 1; i < len(args); i++ {
		if args[i] == "-wait" && i+1 < len(args) {
			fmt.Sscan(args[i+1], &wait) //nolint:errcheck
			i++
			continue
		}
		words = append(words, args[i])
	}
	_ = flag.CommandLine
	var out struct {
		Reply *struct {
			Content   string `json:"content"`
			EventID   string `json:"event_id"`
			LLMCallID string `json:"llm_call_id"`
		} `json:"reply"`
		Note string `json:"note"`
	}
	if code, err := apiRequest("POST", "/api/v1/minds/"+args[0]+"/ask", map[string]any{"text": strings.Join(words, " "), "wait_seconds": wait}, &out); err != nil {
		return exitFor(code, err)
	}
	if out.Reply == nil {
		fmt.Println("(no reply)", out.Note)
		return exitFail
	}
	fmt.Println(out.Reply.Content)
	how := "state"
	if out.Reply.LLMCallID != "" {
		how = "model"
	}
	fmt.Fprintf(os.Stderr, "\n[%s · event %s]\n", how, out.Reply.EventID)
	return exitOK
}
