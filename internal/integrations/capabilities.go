package integrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/pkg/ids"
)

// Capabilities returns the tool surface integrations expose to minds. Each
// executor resolves the named integration for the invoking mind, so a mind
// can only reach endpoints an operator connected to it, never arbitrary ones.
func Capabilities(m *Manager, git GitPoller, prom PrometheusPoller) []capabilities.Capability {
	return []capabilities.Capability{
		{Name: "read_metrics", Description: "Evaluate a PromQL expression against a connected Prometheus integration (target = integration name).", Risk: capabilities.RiskLow, TargetKind: "service", Access: capabilities.AccessRead, Idempotent: true, Timeout: 20 * time.Second,
			SideEffects: "one query against the connected Prometheus", Estimate: capabilities.Cost{WallSeconds: 1, Network: 1},
			Params: map[string]capabilities.Param{"expr": {Type: "string", Required: true, Description: "PromQL expression", MaxLen: 1000}}, DefaultEffect: capabilities.Allow,
			Executor: func(ctx context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
				in, err := m.named(ctx, inv.MindID, Prometheus, inv.Target)
				if err != nil {
					return capabilities.Result{}, err
				}
				token := ""
				if in.SecretRef != "" {
					if token, err = m.Secrets.Resolve(in.SecretRef); err != nil {
						return capabilities.Result{}, fmt.Errorf("resolve secret: %w", err)
					}
				}
				expr, _ := inv.Params["expr"].(string)
				allowPrivate, _ := in.Config["allow_private"].(bool)
				endpoint, _ := in.Config["endpoint"].(string)
				v, labels, err := prom.Query(ctx, endpoint, token, allowPrivate, expr)
				if err != nil {
					return capabilities.Result{}, err
				}
				return capabilities.Result{Summary: fmt.Sprintf("%s = %g", expr, v), Data: map[string]any{"value": v, "labels": labels, "integration_id": in.ID}}, nil
			}},
		{Name: "repository.read", Description: "Read one file from a mirrored git integration (target = integration name; params ref, path).", Risk: capabilities.RiskMedium, TargetKind: "repository", Access: capabilities.AccessRead, Idempotent: true, Timeout: 30 * time.Second,
			SideEffects: "reads a file from the local mirror; nothing is written", Estimate: capabilities.Cost{WallSeconds: 1},
			Params: map[string]capabilities.Param{"path": {Type: "string", Required: true, Description: "path inside the repository", MaxLen: 500}, "ref": {Type: "string", Description: "branch, tag or commit (default HEAD)", MaxLen: 200}}, DefaultEffect: capabilities.Ask,
			Executor: func(ctx context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
				in, err := m.named(ctx, inv.MindID, Git, inv.Target)
				if err != nil {
					return capabilities.Result{}, err
				}
				path, _ := inv.Params["path"].(string)
				ref, _ := inv.Params["ref"].(string)
				if ref == "" {
					ref = "HEAD"
				}
				url, _ := in.Config["url"].(string)
				text, err := git.ReadFile(ctx, url, ref, path, 64<<10)
				if err != nil {
					return capabilities.Result{}, err
				}
				return capabilities.Result{Summary: fmt.Sprintf("%s@%s (%d chars)", path, ref, len(text)), Data: map[string]any{"path": path, "ref": ref, "text": text, "integration_id": in.ID}}, nil
			}},
	}
}

func (m *Manager) named(ctx context.Context, mind ids.ID, kind Kind, name string) (Integration, error) {
	list, err := m.Store.List(ctx, mind)
	if err != nil {
		return Integration{}, err
	}
	for _, in := range list {
		if in.Kind == kind && in.Enabled && (strings.EqualFold(in.Name, name) || in.ID.String() == name) {
			return in, nil
		}
	}
	return Integration{}, errors.New("no enabled " + string(kind) + " integration named " + name + " for this mind")
}
