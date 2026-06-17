package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/pkg/events"
)

func TestObservationAndSSEStream(t *testing.T) {
	srv, tok := testServer(t)
	do := func(method, path, body string) (*http.Response, error) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}
	if res, _ := do("POST", "/api/v1/minds", `{"slug":"sse"}`); res.StatusCode != 201 {
		t.Fatalf("create mind: %d", res.StatusCode)
	}
	// Open the stream first.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/minds/sse/events/stream?type=observation", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream open failed: %v %v", err, res)
	}
	defer res.Body.Close()

	if r, _ := do("POST", "/api/v1/minds/sse/observations", `{"text":"disk latency rising"}`); r.StatusCode != 201 {
		t.Fatalf("observation: %d", r.StatusCode)
	}
	if r, _ := do("POST", "/api/v1/minds/sse/observations", `{"text":""}`); r.StatusCode != 400 {
		t.Fatalf("empty observation should be 400, got %d", r.StatusCode)
	}
	sc := bufio.NewScanner(res.Body)
	var got events.Event
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			if err := json.Unmarshal([]byte(line[6:]), &got); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if got.Type != events.Observation || got.Summary != "disk latency rising" || got.Seq == 0 {
		t.Fatalf("unexpected stream event: %+v", got)
	}
	var payload events.ObservationPayload
	json.Unmarshal(got.Payload, &payload) //nolint:errcheck
	if payload.Trust != events.TrustOperator || payload.Method != "api" {
		t.Fatalf("observation provenance missing: %+v", payload)
	}
	// Forensic context endpoint.
	r, _ := do("GET", "/api/v1/events/"+string(got.ID)+"/context", "")
	var c EventContext
	json.NewDecoder(r.Body).Decode(&c) //nolint:errcheck
	if c.Event.ID != got.ID || c.Children == nil {
		t.Fatalf("context wrong: %+v", c)
	}
}
