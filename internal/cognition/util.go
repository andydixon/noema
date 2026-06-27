package cognition

import (
	"encoding/json"

	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
)

func tokeniseWS(text string) []string { return workspace.Tokenise(text) }

// payloadField reads a string field from an event payload.
func payloadField(ev events.Event, key string) (string, bool) {
	var m map[string]any
	if json.Unmarshal(ev.Payload, &m) != nil {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok
}
