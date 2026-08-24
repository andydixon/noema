package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/llm"
)

// Bedrock speaks the Amazon Bedrock Converse API with SigV4 signing.
// The API key reference resolves to "ACCESS_KEY_ID:SECRET_ACCESS_KEY[:SESSION_TOKEN]".
// The region comes from the endpoint host (bedrock-runtime.<region>.amazonaws.com).
type Bedrock struct {
	cfg    Config
	region string
	creds  awsCredentials
	now    func() time.Time
}

var reBedrockHost = regexp.MustCompile(`^bedrock-runtime(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?$`)

// ErrBedrockConfig marks an unusable provider configuration.
var ErrBedrockConfig = errors.New("bedrock: configuration")

// NewBedrock creates a client. It fails closed on a malformed credential or
// an endpoint that is not a Bedrock runtime host (unless private endpoints
// are explicitly allowed, which is how tests point it at a local server).
func NewBedrock(cfg Config) (*Bedrock, error) {
	creds, ok := parseAWSCredentials(cfg.APIKey)
	if !ok {
		return nil, fmt.Errorf("%w: credential must be ACCESS_KEY_ID:SECRET_ACCESS_KEY[:SESSION_TOKEN]", ErrBedrockConfig)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: endpoint must be a URL", ErrBedrockConfig)
	}
	region := ""
	if m := reBedrockHost.FindStringSubmatch(u.Hostname()); m != nil {
		region = m[1]
	} else if cfg.AllowPrivate {
		region = firstNonEmpty(cfg.Headers["X-Noema-Region"], "us-east-1")
	}
	if region == "" || (u.Scheme != "https" && !cfg.AllowPrivate) {
		return nil, fmt.Errorf("%w: endpoint must be https://bedrock-runtime.<region>.amazonaws.com", ErrBedrockConfig)
	}
	return &Bedrock{cfg: cfg, region: region, creds: creds, now: time.Now}, nil
}

// Complete implements llm.LanguageModel over /model/{id}/converse.
func (p *Bedrock) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	type block struct {
		Text string `json:"text"`
	}
	type msg struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	body := map[string]any{}
	var msgs []msg
	for _, m := range req.Messages {
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		msgs = append(msgs, msg{Role: role, Content: []block{{Text: m.Content}}})
	}
	body["messages"] = msgs
	system := req.System
	if len(req.JSONSchema) > 0 {
		// Converse has no JSON mode switch; the instruction is explicit and the
		// answer is still parsed and validated on our side.
		system = strings.TrimSpace(system + "\nAnswer with a single JSON object and nothing else.")
	}
	if system != "" {
		body["system"] = []block{{Text: system}}
	}
	inf := map[string]any{}
	if req.MaxTokens > 0 {
		inf["maxTokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		inf["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		inf["topP"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		inf["stopSequences"] = req.Stop
	}
	if len(inf) > 0 {
		body["inferenceConfig"] = inf
	}
	var out struct {
		Output struct {
			Message struct {
				Content []block `json:"content"`
			} `json:"message"`
		} `json:"output"`
		StopReason string `json:"stopReason"`
		Usage      struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
			TotalTokens  int `json:"totalTokens"`
		} `json:"usage"`
	}
	start := time.Now()
	if err := p.post(ctx, "/model/"+awsPathSegment(p.cfg.Model)+"/converse", body, &out); err != nil {
		return llm.Response{}, err
	}
	resp := llm.Response{Provider: p.cfg.Name, Model: p.cfg.Model, Latency: time.Since(start), FinishReason: out.StopReason,
		Usage: llm.Usage{PromptTokens: out.Usage.InputTokens, CompletionTokens: out.Usage.OutputTokens, TotalTokens: out.Usage.TotalTokens}}
	var text strings.Builder
	for _, b := range out.Output.Message.Content {
		text.WriteString(b.Text)
	}
	resp.Text = text.String()
	return resp, finish(req, &resp)
}

// post signs and sends a JSON request, decoding JSON and scrubbing errors.
func (p *Bedrock) post(ctx context.Context, path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.url(path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "noema/1")
	for k, v := range p.cfg.Headers {
		if !strings.HasPrefix(k, "X-Noema-") {
			req.Header.Set(k, v)
		}
	}
	signV4(req, body, p.creds, p.region, "bedrock", p.now())
	res, err := p.cfg.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %s", llm.ErrProvider, p.cfg.Name, scrubAWS(err.Error(), p.creds.SecretAccessKey, p.creds.SessionToken))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: %s: HTTP %d: %s", llm.ErrProvider, p.cfg.Name, res.StatusCode, scrubAWS(truncate(string(raw), 400), p.creds.SecretAccessKey, p.creds.SessionToken))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %s: bad JSON response", llm.ErrProvider, p.cfg.Name)
	}
	return nil
}

// scrubAWS removes the credential secrets and anything scrub already covers.
func scrubAWS(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return scrub(s)
}

// awsPathSegment percent-encodes a model id (which may contain ':' or an
// ARN's '/') once for the request path; the signer encodes it again.
func awsPathSegment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z'), (c >= 'a' && c <= 'z'), (c >= '0' && c <= '9'), c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

// BedrockEmbedder calls /model/{id}/invoke for Titan and Cohere embedding models.
type BedrockEmbedder struct {
	p    *Bedrock
	dims int
}

// NewBedrockEmbedder creates an embedder.
func NewBedrockEmbedder(cfg Config, dims int) (*BedrockEmbedder, error) {
	p, err := NewBedrock(cfg)
	if err != nil {
		return nil, err
	}
	return &BedrockEmbedder{p: p, dims: dims}, nil
}

// Model implements embeddings.Embedder.
func (e *BedrockEmbedder) Model() string { return "bedrock/" + e.p.cfg.Model }

// Dims implements embeddings.Embedder.
func (e *BedrockEmbedder) Dims() int { return e.dims }

// Embed implements embeddings.Embedder. Titan takes one text per call;
// Cohere takes a batch.
func (e *BedrockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	path := "/model/" + awsPathSegment(e.p.cfg.Model) + "/invoke"
	if strings.HasPrefix(e.p.cfg.Model, "cohere.") {
		var out struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		if err := e.p.post(ctx, path, map[string]any{"texts": texts, "input_type": "search_document", "truncate": "END"}, &out); err != nil {
			return nil, err
		}
		return out.Embeddings, nil
	}
	var vectors [][]float32
	for _, t := range texts {
		var out struct {
			Embedding []float32 `json:"embedding"`
		}
		if err := e.p.post(ctx, path, map[string]any{"inputText": t}, &out); err != nil {
			return nil, err
		}
		vectors = append(vectors, out.Embedding)
	}
	return vectors, nil
}
