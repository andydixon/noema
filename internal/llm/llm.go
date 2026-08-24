// Package llm is the provider-neutral language-model layer (ADR-0004).
// Cognitive code asks for a *role* ("perceive", "respond", ...); the router
// maps roles to configured models with fallbacks, enforces budgets, records
// every call for inspection and replay, and never lets a secret out.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/andydixon/noema/pkg/ids"
)

// Role names a cognitive task that needs language-model help. Different roles
// may be served by different providers and models.
type Role string

// Roles.
const (
	RolePerceive   Role = "perceive"    // understand a message or document into structured data
	RoleRespond    Role = "respond"     // turn a response intent into language
	RoleHypothesis Role = "hypothesise" // propose explanations and alternatives
	RoleSummarise  Role = "summarise"   // compress episodes, documents, cycles
	RoleJudge      Role = "judge"       // relevance, paraphrase and contradiction judgement
	RoleNarrate    Role = "narrate"     // internal narrative and reflection prose
	RoleEmbed      Role = "embed"       // embeddings for semantic retrieval
)

// RoleInfo documents a role for the configuration UI.
type RoleInfo struct {
	Role        Role   `json:"role"`
	Description string `json:"description"`
	Suggested   string `json:"suggested"` // cheap | strong | local | embedding
}

// Roles lists every role with guidance.
var Roles = []RoleInfo{
	{RolePerceive, "Extract intent, entities, claims and questions from messages and documents into JSON.", "cheap"},
	{RoleRespond, "Generate replies from a structured response intent, personality brief and cited beliefs.", "strong"},
	{RoleHypothesis, "Propose candidate explanations and alternatives for observations (confidence is computed elsewhere).", "strong"},
	{RoleSummarise, "Summarise episodes, documents and cycles into concise text.", "cheap or local"},
	{RoleJudge, "Yes/no and scalar judgements: relevance, paraphrase equivalence, contradiction.", "cheap"},
	{RoleNarrate, "Write the internal narrative and reflection notes.", "cheap or local"},
	{RoleEmbed, "Embed text for semantic retrieval.", "embedding"},
}

// Message is one turn in a chat-style request.
type Message struct {
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
}

// Request is a provider-neutral completion request.
type Request struct {
	Role          Role            `json:"role"`
	PromptName    string          `json:"prompt_name,omitempty"`
	PromptVersion int             `json:"prompt_version,omitempty"`
	System        string          `json:"system,omitempty"`
	Messages      []Message       `json:"messages"`
	JSONSchema    json.RawMessage `json:"json_schema,omitempty"` // when set, the model must answer with JSON matching it
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Seed          *int64          `json:"seed,omitempty"`
	Stop          []string        `json:"stop,omitempty"`
	Timeout       time.Duration   `json:"-"`
	// Provenance, recorded with the call.
	MindID        ids.ID `json:"mind_id,omitempty"`
	CorrelationID ids.ID `json:"correlation_id,omitempty"`
	Purpose       string `json:"purpose,omitempty"`
}

// Usage is token accounting.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is a provider-neutral completion.
type Response struct {
	Text         string          `json:"text"`
	JSON         json.RawMessage `json:"json,omitempty"` // parsed object when JSON was requested and valid
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Usage        Usage           `json:"usage"`
	Latency      time.Duration   `json:"latency"`
	FinishReason string          `json:"finish_reason,omitempty"`
	CallID       ids.ID          `json:"call_id,omitempty"`
	Replayed     bool            `json:"replayed,omitempty"` // served from a recording (exact replay)
	Cost         float64         `json:"cost,omitempty"`
}

// LanguageModel is implemented by providers. It knows one endpoint and one
// model; routing across models happens above it.
type LanguageModel interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Errors.
var (
	ErrNoModel        = errors.New("llm: no model configured for role")
	ErrBudgetExceeded = errors.New("llm: token budget exceeded")
	ErrProvider       = errors.New("llm: provider error")
	ErrBadJSON        = errors.New("llm: model did not return valid JSON")
)

// ProviderKind identifies a wire protocol.
type ProviderKind string

// Provider kinds. "openai" covers every OpenAI-compatible server: OpenAI,
// Azure-style gateways, llama.cpp server, vLLM, LM Studio, Ollama's /v1.
const (
	KindOpenAI    ProviderKind = "openai"
	KindAnthropic ProviderKind = "anthropic"
	KindOllama    ProviderKind = "ollama"
	KindBedrock   ProviderKind = "bedrock" // Amazon Bedrock Converse API, SigV4-signed
)

// Valid reports whether k is supported.
func (k ProviderKind) Valid() bool {
	return k == KindOpenAI || k == KindAnthropic || k == KindOllama || k == KindBedrock
}

// DefaultEndpoint suggests a base URL per kind.
func (k ProviderKind) DefaultEndpoint() string {
	switch k {
	case KindOpenAI:
		return "https://api.openai.com/v1"
	case KindAnthropic:
		return "https://api.anthropic.com"
	case KindOllama:
		return "http://127.0.0.1:11434"
	case KindBedrock:
		return "https://bedrock-runtime.eu-west-2.amazonaws.com"
	}
	return ""
}
