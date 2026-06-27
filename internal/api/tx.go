package api

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/andydixon/noema/internal/storage"
)

// txQuerier is what handlers may run inside an API transaction.
type txQuerier = storage.Querier

// txDo runs fn in a transaction on the shared pool.
func txDo(r *http.Request, s *Server, fn func(q txQuerier) error) error {
	return storage.Tx(r.Context(), s.deps.DB, func(tx pgx.Tx) error { return fn(tx) })
}
