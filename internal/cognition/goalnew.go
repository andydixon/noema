package cognition

import (
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/pkg/ids"
)

func goalNew(mind ids.ID, title, subject string, priority float64, origin ids.ID) goals.New {
	return goals.New{MindID: mind, Title: title, Kind: goals.KindInvestigation, Priority: priority, Subject: subject, Owner: "mind", OriginEvent: origin}
}
