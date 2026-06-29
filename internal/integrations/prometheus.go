package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/security/httpclient"
	"github.com/andydixon/noema/internal/security/secrets"
)

// PrometheusPoller evaluates instant queries against a Prometheus-compatible
// API (Prometheus, VictoriaMetrics, Thanos) and emits observations when a
// threshold is crossed or a value moves by more than the configured change
// ratio, so steady metrics do not flood cognition.
type PrometheusPoller struct {
	Secrets *secrets.Resolver
}

type promQuery struct {
	Name      string  `json:"name"`
	Expr      string  `json:"expr"`
	Threshold float64 `json:"threshold"`
	Compare   string  `json:"compare"` // gt | lt | "" (change-only)
	Change    float64 `json:"change"`  // relative change that counts as notable (default 0.2)
	Unit      string  `json:"unit"`
}

// Query runs one instant query and returns the scalar value of the first
// sample (or an error when the result is empty or not numeric).
func (p PrometheusPoller) Query(ctx context.Context, endpoint, token string, allowPrivate bool, expr string) (float64, map[string]string, error) {
	client := httpclient.New(httpclient.Options{Timeout: 20 * time.Second, AllowPrivate: allowPrivate, MaxBody: 1 << 20})
	u := strings.TrimRight(endpoint, "/") + "/api/v1/query?query=" + url.QueryEscape(expr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, err := httpclient.ReadAll(res.Body, 1<<20)
	if err != nil {
		return 0, nil, err
	}
	if res.StatusCode != 200 {
		return 0, nil, fmt.Errorf("HTTP %d from metrics endpoint", res.StatusCode)
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Status != "success" {
		return 0, nil, fmt.Errorf("unexpected metrics response")
	}
	if len(out.Data.Result) == 0 || len(out.Data.Result[0].Value) < 2 {
		return 0, nil, fmt.Errorf("empty result")
	}
	v, err := strconv.ParseFloat(fmt.Sprint(out.Data.Result[0].Value[1]), 64)
	if err != nil {
		return 0, nil, fmt.Errorf("non-numeric result")
	}
	return v, out.Data.Result[0].Metric, nil
}

// Poll implements Poller.
func (p PrometheusPoller) Poll(ctx context.Context, in Integration, sink *Sink) error {
	endpoint, _ := in.Config["endpoint"].(string)
	allowPrivate, _ := in.Config["allow_private"].(bool)
	token := ""
	if in.SecretRef != "" && p.Secrets != nil {
		var err error
		if token, err = p.Secrets.Resolve(in.SecretRef); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(in.Config["queries"])
	var queries []promQuery
	json.Unmarshal(raw, &queries) //nolint:errcheck
	last, _ := in.State["last_values"].(map[string]any)
	if last == nil {
		last = map[string]any{}
	}
	host := hostOf(endpoint)
	for _, q := range queries {
		sink.Item()
		v, labels, err := p.Query(ctx, endpoint, token, allowPrivate, q.Expr)
		if err != nil {
			sink.Fail(fmt.Errorf("%s: %w", q.Name, err))
			continue
		}
		prev, had := last[q.Name].(float64)
		last[q.Name] = v
		change := q.Change
		if change <= 0 {
			change = 0.2
		}
		crossed := (q.Compare == "gt" && v > q.Threshold) || (q.Compare == "lt" && v < q.Threshold)
		wasCrossed := had && ((q.Compare == "gt" && prev > q.Threshold) || (q.Compare == "lt" && prev < q.Threshold))
		moved := had && math.Abs(v-prev) > change*math.Max(math.Abs(prev), 1e-9)
		if !had || crossed != wasCrossed || moved {
			sal := 0.4
			verb := "is"
			if crossed && !wasCrossed {
				sal, verb = 0.8, "crossed its threshold at"
			} else if !crossed && wasCrossed {
				sal, verb = 0.6, "returned within threshold at"
			} else if moved {
				sal, verb = 0.55, "changed to"
			}
			instance := labels["instance"]
			text := fmt.Sprintf("metric %s (%s) %s %s%s", q.Name, q.Expr, verb, formatNum(v), q.Unit)
			if had {
				text += fmt.Sprintf(" (was %s%s)", formatNum(prev), q.Unit)
			}
			if instance != "" {
				text += " on " + instance
			}
			ents := []string{host}
			if instance != "" {
				ents = append(ents, strings.Split(instance, ":")[0])
			}
			if _, err := sink.Observe(ctx, Observation{Summary: text, Text: text, Reference: endpoint + " " + q.Expr, ContentType: "text/plain", Salience: sal,
				Metadata: map[string]any{"metric": q.Name, "value": v, "previous": prev, "threshold": q.Threshold, "compare": q.Compare, "instance": instance}, Entities: ents, EntityKind: graph.System}); err != nil {
				sink.Fail(err)
			}
		}
	}
	return sink.Store.SetState(ctx, in.ID, map[string]any{"last_values": last, "last_poll": time.Now().Format(time.RFC3339)})
}

func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	return u.Hostname()
}

func formatNum(v float64) string {
	if math.Abs(v) >= 1000 || v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}
