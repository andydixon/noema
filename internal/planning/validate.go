package planning

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/andydixon/noema/internal/capabilities"
)

// Validation is the result of checking a draft.
type Validation struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
	Order    []string `json:"order"` // a topological order of step keys
}

// Limits on plans.
const (
	MaxSteps          = 30
	MaxWaitSeconds    = 7 * 24 * 3600
	MaxMonitorSeconds = 7 * 24 * 3600
)

var keyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,16}$`)

// Validate checks structure, dependency integrity and the capability
// vocabulary. Nothing a draft says is trusted: capability names must be
// registered and not locked, parameters must satisfy the schema, targets
// must agree with parameters, checks must use read-only capabilities, and
// the dependency graph must be acyclic.
func Validate(d *Draft, reg *capabilities.Registry) Validation {
	v := Validation{Errors: []string{}, Warnings: []string{}, Order: []string{}}
	fail := func(format string, args ...any) { v.Errors = append(v.Errors, fmt.Sprintf(format, args...)) }
	d.Objective = strings.TrimSpace(d.Objective)
	if d.Objective == "" || len(d.Objective) > 500 {
		fail("objective is required (at most 500 characters)")
	}
	switch d.Origin {
	case "template", "model", "operator", "dream", "replan":
	default:
		fail("origin %q is not recognised", d.Origin)
	}
	if len(d.Steps) == 0 || len(d.Steps) > MaxSteps {
		fail("a plan needs 1–%d steps, not %d", MaxSteps, len(d.Steps))
	}
	if d.Budget.MaxInvocations <= 0 {
		d.Budget.MaxInvocations = 20
	}
	keys := map[string]int{}
	for i := range d.Steps {
		st := &d.Steps[i]
		st.Title = strings.TrimSpace(st.Title)
		if !keyRe.MatchString(st.Key) {
			fail("step %d: key %q must be 1–16 letters, digits, '-' or '_'", i+1, st.Key)
			continue
		}
		if _, dup := keys[st.Key]; dup {
			fail("step %s: duplicate key", st.Key)
		}
		keys[st.Key] = i
		if st.Title == "" || len(st.Title) > 200 {
			fail("step %s: title is required (at most 200 characters)", st.Key)
		}
		if st.MaxAttempts <= 0 {
			st.MaxAttempts = 1
		}
		if st.MaxAttempts > 5 {
			st.MaxAttempts = 5
		}
	}
	for i := range d.Steps {
		st := &d.Steps[i]
		for _, dep := range st.DependsOn {
			if dep == st.Key {
				fail("step %s depends on itself", st.Key)
			} else if _, ok := keys[dep]; !ok {
				fail("step %s depends on %q, which is not a step of this plan", st.Key, dep)
			}
		}
		validateStep(st, d.Steps, keys, reg, fail, &v)
	}
	if len(v.Errors) == 0 {
		order, err := topo(d.Steps)
		if err != nil {
			fail("%v", err)
		} else {
			v.Order = order
		}
	}
	for _, a := range d.Assumptions {
		if a.MinConfidence < 0 || a.MinConfidence > 1 {
			fail("assumption %q: min_confidence must be in [0,1]", a.Proposition)
		}
	}
	v.Valid = len(v.Errors) == 0
	return v
}

func validateStep(st *StepSpec, all []StepSpec, keys map[string]int, reg *capabilities.Registry, fail func(string, ...any), v *Validation) {
	registered := func(name string) (capabilities.Capability, bool) {
		c, ok := reg.Get(name)
		if !ok {
			fail("step %s: %q is not a registered capability", st.Key, name)
			return c, false
		}
		if c.Locked {
			fail("step %s: %s is locked and can never run", st.Key, name)
			return c, false
		}
		return c, true
	}
	checkCheck := func(label string, ck *Check) {
		c, ok := registered(ck.Capability)
		if !ok {
			return
		}
		if c.Access != capabilities.AccessRead {
			fail("step %s: %s check must use a read-only capability, not %s", st.Key, label, c.Name)
		}
		p, err := c.ValidateParams(ck.Params)
		if err != nil {
			fail("step %s: %s check parameters: %v", st.Key, label, err)
			return
		}
		ck.Params = p
		if ck.Target, err = c.ResolveTarget(ck.Target, p); err != nil {
			fail("step %s: %s check target: %v", st.Key, label, err)
		}
		switch ck.Op {
		case "eq", "ne", "lt", "lte", "gt", "gte", "contains", "exists":
		default:
			fail("step %s: %s check operator %q is not recognised", st.Key, label, ck.Op)
		}
		if ck.Path == "" {
			fail("step %s: %s check needs a result path", st.Key, label)
		}
	}
	populated := 0
	for _, x := range []bool{st.Request != nil, st.Verify != nil, st.Monitor != nil, st.Wait != nil, st.Internal != nil} {
		if x {
			populated++
		}
	}
	if populated > 1 {
		fail("step %s: more than one kind-specific section", st.Key)
	}
	switch st.Kind {
	case KindCapability:
		c, ok := registered(st.Capability)
		if !ok {
			return
		}
		p, err := c.ValidateParams(st.Params)
		if err != nil {
			fail("step %s: parameters: %v", st.Key, err)
			return
		}
		st.Params = p
		if st.Target, err = c.ResolveTarget(st.Target, p); err != nil {
			fail("step %s: target: %v", st.Key, err)
		}
		if c.Access == capabilities.AccessWrite && !c.Idempotent && st.MaxAttempts > 1 {
			v.Warnings = append(v.Warnings, fmt.Sprintf("step %s: %s is not idempotent; retries reduced to one attempt", st.Key, c.Name))
			st.MaxAttempts = 1
		}
		if st.Expectation != nil {
			if st.Expectation.Confidence <= 0 || st.Expectation.Confidence >= 1 {
				st.Expectation.Confidence = 0.7
			}
			if st.Expectation.WithinSeconds <= 0 || st.Expectation.WithinSeconds > 86400 {
				st.Expectation.WithinSeconds = 300
			}
			if strings.TrimSpace(st.Expectation.Description) == "" {
				fail("step %s: an expectation needs a description", st.Key)
			}
		} else if c.Access == capabilities.AccessWrite {
			v.Warnings = append(v.Warnings, fmt.Sprintf("step %s: %s changes the world but states no expected outcome", st.Key, c.Name))
		}
		if st.Precondition != nil {
			checkCheck("precondition", &st.Precondition.Check)
		}
	case KindRequestCapability:
		if st.Request == nil {
			fail("step %s: request_capability needs a request", st.Key)
			return
		}
		if _, ok := registered(st.Request.Capability); !ok {
			return
		}
		if st.Request.DurationSeconds <= 0 || st.Request.DurationSeconds > int(capabilities.MaxGrantDuration.Seconds()) {
			fail("step %s: request duration must be 1 second to 7 days", st.Key)
		}
		if strings.TrimSpace(st.Request.Reason) == "" {
			fail("step %s: a capability request needs a reason", st.Key)
		}
	case KindVerify:
		if st.Verify == nil {
			fail("step %s: verify needs a verify section", st.Key)
			return
		}
		i, ok := keys[st.Verify.Of]
		if !ok {
			fail("step %s: verifies %q, which is not a step", st.Key, st.Verify.Of)
			return
		}
		if all[i].Expectation == nil {
			fail("step %s: step %s has no expectation to verify", st.Key, st.Verify.Of)
		}
		if !dependsOn(all, keys, st.Key, st.Verify.Of) {
			fail("step %s: must depend on %s, the step it verifies", st.Key, st.Verify.Of)
		}
		checkCheck("verify", &st.Verify.Check)
		if st.Verify.IntervalSeconds <= 0 {
			st.Verify.IntervalSeconds = 10
		}
		if st.Verify.WithinSeconds <= 0 {
			st.Verify.WithinSeconds = 60
		}
	case KindMonitor:
		if st.Monitor == nil {
			fail("step %s: monitor needs a monitor section", st.Key)
			return
		}
		checkCheck("monitor", &st.Monitor.Check)
		m := st.Monitor
		if m.IntervalSeconds < 5 {
			m.IntervalSeconds = 5
		}
		if m.DeadlineSeconds <= 0 || m.DeadlineSeconds > MaxMonitorSeconds || m.StableSeconds < 0 || m.StableSeconds > m.DeadlineSeconds {
			fail("step %s: monitor needs 0 ≤ stable ≤ deadline ≤ 7 days", st.Key)
		}
	case KindWait:
		if st.Wait == nil || st.Wait.Seconds <= 0 || st.Wait.Seconds > MaxWaitSeconds {
			fail("step %s: wait needs 1 second to 7 days", st.Key)
		}
	case KindInternal:
		if st.Internal == nil || InternalActions[st.Internal.Action] == "" {
			fail("step %s: internal action must be one of %s", st.Key, strings.Join(sortedKeys(InternalActions), ", "))
		}
	default:
		fail("step %s: kind %q is not recognised", st.Key, st.Kind)
	}
	if st.Kind != KindCapability && (st.Capability != "" || st.Expectation != nil) {
		v.Warnings = append(v.Warnings, fmt.Sprintf("step %s: capability and expectation are ignored on %s steps", st.Key, st.Kind))
		st.Capability, st.Target, st.Params, st.Expectation = "", "", nil, nil
	}
}

func dependsOn(all []StepSpec, keys map[string]int, from, to string) bool {
	seen := map[string]bool{}
	var walk func(k string) bool
	walk = func(k string) bool {
		if seen[k] {
			return false
		}
		seen[k] = true
		i, ok := keys[k]
		if !ok {
			return false
		}
		for _, d := range all[i].DependsOn {
			if d == to || walk(d) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

// ErrCycle marks cyclic dependencies.
var ErrCycle = errors.New("dependencies form a cycle")

func topo(steps []StepSpec) ([]string, error) {
	indeg := map[string]int{}
	next := map[string][]string{}
	for _, s := range steps {
		indeg[s.Key] += 0
		for _, d := range s.DependsOn {
			indeg[s.Key]++
			next[d] = append(next[d], s.Key)
		}
	}
	var queue, order []string
	for _, s := range steps {
		if indeg[s.Key] == 0 {
			queue = append(queue, s.Key)
		}
	}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		order = append(order, k)
		for _, n := range next[k] {
			indeg[n]--
			if indeg[n] == 0 {
				queue = append(queue, n)
			}
		}
	}
	if len(order) != len(steps) {
		var stuck []string
		for _, s := range steps {
			if indeg[s.Key] > 0 {
				stuck = append(stuck, s.Key)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("%w among %s", ErrCycle, strings.Join(stuck, ", "))
	}
	return order, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Evaluate applies a check to a capability result's data. It returns whether
// the check holds and the observed value.
func Evaluate(ck Check, data map[string]any) (bool, any, error) {
	val, ok := lookup(data, ck.Path)
	if ck.Op == "exists" {
		return ok, val, nil
	}
	if !ok {
		return false, nil, fmt.Errorf("result has no %q", ck.Path)
	}
	switch ck.Op {
	case "eq":
		return equal(val, ck.Value), val, nil
	case "ne":
		return !equal(val, ck.Value), val, nil
	case "contains":
		return strings.Contains(strings.ToLower(fmt.Sprint(val)), strings.ToLower(fmt.Sprint(ck.Value))), val, nil
	case "lt", "lte", "gt", "gte":
		a, aok := number(val)
		b, bok := number(ck.Value)
		if !aok || !bok {
			return false, val, fmt.Errorf("%q is not numeric", ck.Path)
		}
		switch ck.Op {
		case "lt":
			return a < b, val, nil
		case "lte":
			return a <= b, val, nil
		case "gt":
			return a > b, val, nil
		}
		return a >= b, val, nil
	}
	return false, val, fmt.Errorf("unknown operator %q", ck.Op)
}

func lookup(data map[string]any, path string) (any, bool) {
	var cur any = data
	for _, part := range strings.Split(path, ".") {
		switch m := cur.(type) {
		case map[string]any:
			v, ok := m[part]
			if !ok {
				return nil, false
			}
			cur = v
		case map[string]string:
			v, ok := m[part]
			if !ok {
				return nil, false
			}
			cur = v
		default:
			return nil, false
		}
	}
	return cur, true
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func equal(a, b any) bool {
	if fa, ok := number(a); ok {
		if fb, ok := number(b); ok {
			return fa == fb
		}
	}
	if reflect.DeepEqual(a, b) {
		return true
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}
