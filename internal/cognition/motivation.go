package cognition

import (
	"context"
	"fmt"
	"math"

	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/pkg/events"
)

// MotivationHook applies the deterministic affect and drive rules to the
// events a cycle produced, persists the result, and emits emotion_changed or
// drive_changed events only when something moved noticeably.
type MotivationHook struct{ Store *motivation.Store }

// Name implements brain.Hook.
func (MotivationHook) Name() string { return "motivation" }

// AfterCycle implements brain.Hook.
func (h MotivationHook) AfterCycle(ctx context.Context, c *brain.Cycle, emitted []events.Event) error {
	if h.Store == nil {
		return nil
	}
	st := c.State.Motiv
	if len(st.Drives) == 0 {
		return nil
	}
	before := st
	before.Drives = append([]motivation.Drive{}, st.Drives...)
	var reasons []string
	all := append(append([]events.Event{}, c.State.NewEvents...), emitted...)
	for _, ev := range all {
		if d, ok := motivation.Rules(ev); ok {
			motivation.Apply(&st, d)
			reasons = append(reasons, d.Reason)
		}
	}
	st.Affect.UpdatedAt = c.State.Now
	if err := h.Store.SaveAffect(ctx, c.Tx, c.State.Mind.ID, st.Affect); err != nil {
		return err
	}
	for i, d := range st.Drives {
		if math.Abs(d.Value-before.Drives[i].Value) > 1e-9 {
			if err := h.Store.SaveDrive(ctx, c.Tx, c.State.Mind.ID, d); err != nil {
				return err
			}
			if math.Abs(d.Value-before.Drives[i].Value) >= 0.1 {
				c.Emit(events.New{Type: events.DriveChanged, Source: "brain.motivation", Summary: fmt.Sprintf("drive %s %.2f → %.2f", d.Name, before.Drives[i].Value, d.Value), Salience: 0.2, Confidence: 1,
					Metadata: map[string]any{"drive": d.Name, "from": before.Drives[i].Value, "to": d.Value}, Payload: map[string]any{"drive": d, "reasons": uniq(reasons)}})
			}
		}
	}
	if key, sig := motivation.Significant(before.Affect, st.Affect); sig {
		v := st.Affect.Valence
		a := st.Affect.Arousal
		c.Emit(events.New{Type: events.EmotionChanged, Source: "brain.motivation", Summary: fmt.Sprintf("cognitive state shifted: %s now %.2f (%s)", key, field(st.Affect, key), joinReasons(reasons)),
			Salience: 0.3, Confidence: 1, Valence: &v, Arousal: &a, Metadata: map[string]any{"field": key}, Payload: map[string]any{"before": before.Affect, "after": st.Affect, "reasons": uniq(reasons)}})
	}
	return nil
}

func field(a motivation.Affect, key string) float64 {
	switch key {
	case "valence":
		return a.Valence
	case "arousal":
		return a.Arousal
	case "curiosity":
		return a.Curiosity
	case "concern":
		return a.Concern
	case "frustration":
		return a.Frustration
	case "confidence":
		return a.Confidence
	case "uncertainty":
		return a.Uncertainty
	case "engagement":
		return a.Engagement
	case "task_pressure":
		return a.TaskPressure
	}
	return a.Load
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func joinReasons(xs []string) string {
	u := uniq(xs)
	if len(u) > 3 {
		u = append(u[:3], "…")
	}
	s := ""
	for i, x := range u {
		if i > 0 {
			s += ", "
		}
		s += x
	}
	return s
}
