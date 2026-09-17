package research

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/andydixon/noema/pkg/ids"
)

// AgencyMetrics are the C10 rates for one mind over a period.
type AgencyMetrics struct {
	Since                     time.Time      `json:"since"`
	WantsCreated              int            `json:"wants_created"`
	WantToIntention           float64        `json:"want_to_intention_rate"`
	IntentionsFormed          int            `json:"intentions_formed"`
	IntentionToAction         float64        `json:"intention_to_action_rate"`
	IntentionAbandonment      float64        `json:"intention_abandonment_rate"`
	CommitmentsFulfilled      int            `json:"commitments_fulfilled"`
	CommitmentsBroken         int            `json:"commitments_broken"`
	CommitmentFulfilment      float64        `json:"commitment_fulfilment_rate"`
	CommitmentConflictPerItem float64        `json:"commitment_conflict_frequency"`
	BlockedIntentions         float64        `json:"blocked_intention_frequency"`
	PlansCreated              int            `json:"plans_created"`
	Replanning                float64        `json:"replanning_rate"`
	OutcomeSuccess            float64        `json:"outcome_success_rate"`
	IntrospectionAccuracy     float64        `json:"introspection_accuracy"`
	IntrospectionReplies      int            `json:"introspection_replies"`
	Attribution               map[string]int `json:"attribution,omitempty"`
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return round3(float64(a) / float64(b))
}

// Agency computes AgencyMetrics from volition, planning, execution and
// measurement records.
func (s *Store) Agency(ctx context.Context, mind ids.ID, since time.Time) (AgencyMetrics, error) {
	m := AgencyMetrics{Since: since}
	q := func(sql string, dst ...any) {
		s.db.QueryRow(ctx, sql, mind, since).Scan(dst...) //nolint:errcheck
	}
	var wantsWithIntention, withAction, abandoned, blocked, conflicts, commitments, revised, completedOK, closed int
	q(`SELECT count(*), count(*) FILTER (WHERE EXISTS (SELECT 1 FROM intentions i WHERE i.mind_id=w.mind_id AND w.id = ANY(i.source_wants))) FROM wants w WHERE w.mind_id=$1 AND w.created_at >= $2`, &m.WantsCreated, &wantsWithIntention)
	q(`SELECT count(*),
		count(*) FILTER (WHERE EXISTS (SELECT 1 FROM plans p JOIN plan_steps s ON s.plan_id=p.id WHERE p.intention_id=i.id AND (s.execution_id IS NOT NULL OR s.approval_id IS NOT NULL OR s.request_id IS NOT NULL))),
		count(*) FILTER (WHERE i.status='abandoned')
		FROM intentions i WHERE i.mind_id=$1 AND i.created_at >= $2 AND i.parent_id IS NULL`, &m.IntentionsFormed, &withAction, &abandoned)
	q(`SELECT count(DISTINCT (metadata->>'intention_id')) FROM cognitive_events WHERE mind_id=$1 AND type='intention_blocked' AND occurred_at >= $2`, &blocked)
	q(`SELECT count(*) FILTER (WHERE status='fulfilled'), count(*) FILTER (WHERE status='broken') FROM commitments WHERE mind_id=$1 AND coalesce(closed_at, created_at) >= $2`, &m.CommitmentsFulfilled, &m.CommitmentsBroken)
	q(`SELECT count(*) FROM cognitive_events WHERE mind_id=$1 AND type='commitment_conflict' AND occurred_at >= $2`, &conflicts)
	q(`SELECT count(*) FROM commitments WHERE mind_id=$1 AND created_at >= $2`, &commitments)
	q(`SELECT count(*), count(*) FILTER (WHERE revision > 1) FROM plans WHERE mind_id=$1 AND created_at >= $2`, &m.PlansCreated, &revised)
	q(`SELECT count(*) FILTER (WHERE status='completed' AND strategy <> 'diagnose_then_escalate'), count(*) FILTER (WHERE status IN ('completed','failed','abandoned')) FROM plans WHERE mind_id=$1 AND coalesce(closed_at, created_at) >= $2`, &completedOK, &closed)
	var acc *float64
	q(`SELECT avg(value), count(*) FROM research_measurements WHERE mind_id=$1 AND measure='introspection_accuracy' AND created_at >= $2`, &acc, &m.IntrospectionReplies)
	if acc != nil {
		m.IntrospectionAccuracy = round3(*acc)
	}
	m.WantToIntention = ratio(wantsWithIntention, m.WantsCreated)
	m.IntentionToAction = ratio(withAction, m.IntentionsFormed)
	m.IntentionAbandonment = ratio(abandoned, m.IntentionsFormed)
	m.CommitmentFulfilment = ratio(m.CommitmentsFulfilled, m.CommitmentsFulfilled+m.CommitmentsBroken)
	m.CommitmentConflictPerItem = ratio(conflicts, commitments)
	m.BlockedIntentions = ratio(blocked, m.IntentionsFormed)
	m.Replanning = ratio(revised, m.PlansCreated)
	m.OutcomeSuccess = ratio(completedOK, closed)
	return m, nil
}

// CalibrationBucket is one confidence band.
type CalibrationBucket struct {
	From           float64 `json:"from"`
	To             float64 `json:"to"`
	Cited          int     `json:"cited"`
	Revised        int     `json:"revised"`
	RevisedRate    float64 `json:"revised_rate"`
	MeanConfidence float64 `json:"mean_confidence"`
}

// Calibration is the C6 metacognitive calibration report.
type Calibration struct {
	Since               time.Time           `json:"since"`
	Buckets             []CalibrationBucket `json:"buckets"`
	Brier               float64             `json:"brier"`
	Samples             int                 `json:"samples"`
	UncertainRevised    float64             `json:"expressed_uncertainty_revised_rate"`
	UncertainSamples    int                 `json:"expressed_uncertainty_samples"`
	ConfidentRevised    float64             `json:"no_uncertainty_revised_rate"`
	ConfidentSamples    int                 `json:"no_uncertainty_samples"`
	BrierFirstHalf      float64             `json:"brier_first_half"`
	BrierSecondHalf     float64             `json:"brier_second_half"`
	ImprovedOverTime    *bool               `json:"improved_over_time,omitempty"`
	RevisionWindowHours int                 `json:"revision_window_hours"`
}

// RevisionWindow is how long after a reply a revision counts.
const RevisionWindow = 7 * 24 * time.Hour

// MetacognitiveCalibration relates the confidence and uncertainty a mind
// expressed in replies to whether the cited beliefs were later revised.
func (s *Store) MetacognitiveCalibration(ctx context.Context, mind ids.ID, since time.Time) (Calibration, error) {
	c := Calibration{Since: since, RevisionWindowHours: int(RevisionWindow.Hours())}
	rows, err := s.db.Query(ctx, `SELECT occurred_at, payload FROM cognitive_events WHERE mind_id=$1 AND type='response_generated' AND occurred_at >= $2 ORDER BY occurred_at`, mind, since)
	if err != nil {
		return c, err
	}
	type sample struct {
		at        time.Time
		conf      float64
		revised   bool
		uncertain bool
	}
	var samples []sample
	type cited struct {
		at        time.Time
		belief    ids.ID
		conf      float64
		status    string
		uncertain bool
	}
	var all []cited
	for rows.Next() {
		var at time.Time
		var raw []byte
		if rows.Scan(&at, &raw) != nil {
			continue
		}
		var p struct {
			Intent struct {
				Beliefs []struct {
					ID         ids.ID  `json:"id"`
					Confidence float64 `json:"confidence"`
					Status     string  `json:"status"`
				} `json:"beliefs"`
				Uncertainty string `json:"uncertainty"`
			} `json:"intent"`
		}
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		for _, b := range p.Intent.Beliefs {
			all = append(all, cited{at, b.ID, b.Confidence, b.Status, p.Intent.Uncertainty != ""})
		}
	}
	rows.Close()
	for _, ct := range all {
		var n int
		s.db.QueryRow(ctx, `SELECT count(*) FROM belief_versions WHERE belief_id=$1 AND created_at > $2 AND created_at <= $3 AND (status <> $4 OR abs(confidence - $5) > 0.2)`, ct.belief, ct.at, ct.at.Add(RevisionWindow), ct.status, ct.conf).Scan(&n) //nolint:errcheck
		samples = append(samples, sample{ct.at, ct.conf, n > 0, ct.uncertain})
	}
	c.Samples = len(samples)
	c.Buckets = make([]CalibrationBucket, 5)
	for i := range c.Buckets {
		c.Buckets[i].From, c.Buckets[i].To = float64(i)/5, float64(i+1)/5
	}
	var brier, first, second float64
	var nFirst, nSecond, unc, uncRev, conf, confRev int
	for i, sm := range samples {
		b := int(math.Min(4, math.Floor(sm.conf*5)))
		c.Buckets[b].Cited++
		c.Buckets[b].MeanConfidence += sm.conf
		stays := 1.0
		if sm.revised {
			c.Buckets[b].Revised++
			stays = 0
		}
		sq := (sm.conf - stays) * (sm.conf - stays)
		brier += sq
		if i < len(samples)/2 {
			first += sq
			nFirst++
		} else {
			second += sq
			nSecond++
		}
		if sm.uncertain {
			unc++
			if sm.revised {
				uncRev++
			}
		} else {
			conf++
			if sm.revised {
				confRev++
			}
		}
	}
	for i := range c.Buckets {
		if c.Buckets[i].Cited > 0 {
			c.Buckets[i].RevisedRate = ratio(c.Buckets[i].Revised, c.Buckets[i].Cited)
			c.Buckets[i].MeanConfidence = round3(c.Buckets[i].MeanConfidence / float64(c.Buckets[i].Cited))
		}
	}
	if c.Samples > 0 {
		c.Brier = round3(brier / float64(c.Samples))
	}
	c.UncertainSamples, c.ConfidentSamples = unc, conf
	c.UncertainRevised, c.ConfidentRevised = ratio(uncRev, unc), ratio(confRev, conf)
	if nFirst >= 5 && nSecond >= 5 {
		c.BrierFirstHalf, c.BrierSecondHalf = round3(first/float64(nFirst)), round3(second/float64(nSecond))
		improved := c.BrierSecondHalf < c.BrierFirstHalf
		c.ImprovedOverTime = &improved
	}
	return c, nil
}

// SelfModelAccuracy compares a self-model's capability list with the
// effective policy.
func SelfModelAccuracy(stated map[string]string, effective map[string]string) (float64, []string) {
	if len(stated) == 0 {
		return 0, nil
	}
	var wrong []string
	right := 0
	for name, policy := range stated {
		eff := effective[name]
		if eff == "locked" {
			eff = "unavailable"
		}
		if eff == policy {
			right++
		} else {
			wrong = append(wrong, name+": stated "+policy+", actual "+eff)
		}
	}
	return round3(float64(right) / float64(len(stated))), wrong
}
