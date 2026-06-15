package api

import (
	"errors"

	"github.com/andydixon/noema/internal/storage"
)

func isStorageErr(err error) bool {
	return errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrConflict)
}
