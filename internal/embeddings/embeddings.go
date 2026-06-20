// Package embeddings defines the embedding provider interface and helpers for
// storing vectors in pgvector. Providers arrive with the LLM layer; None keeps
// the system fully functional without them (spec §46).
package embeddings

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// Embedder turns texts into vectors.
type Embedder interface {
	// Model returns a stable identifier such as "openai/text-embedding-3-small"
	// so vectors from different models are never compared.
	Model() string
	Dims() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// ErrUnavailable is returned by None.
var ErrUnavailable = errors.New("embeddings unavailable")

// None is the embedder used when no provider is configured.
type None struct{}

// Model implements Embedder.
func (None) Model() string { return "" }

// Dims implements Embedder.
func (None) Dims() int { return 0 }

// Embed implements Embedder.
func (None) Embed(context.Context, []string) ([][]float32, error) { return nil, ErrUnavailable }

// Format renders a vector in pgvector's text syntax.
func Format(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
