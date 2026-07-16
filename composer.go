package tusdfiber

import (
	"fmt"

	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// StoreComposer is a thin wrapper around tusd's StoreComposer.
// It composes core data stores with optional extensions (locker, terminator, etc.).
type StoreComposer struct {
	*tusd.StoreComposer
}

// NewStoreComposer creates a fresh StoreComposer.
func NewStoreComposer() *StoreComposer {
	return &StoreComposer{
		StoreComposer: tusd.NewStoreComposer(),
	}
}

// Capabilities returns a human-readable string of supported extensions.
func (sc *StoreComposer) Capabilities() string {
	str := fmt.Sprintf("Core: %v Terminater: %v Locker: %v Concater: %v LengthDeferrer: %v",
		sc.Core != nil,
		sc.UsesTerminater,
		sc.UsesLocker,
		sc.UsesConcater,
		sc.UsesLengthDeferrer,
	)
	return str
}
