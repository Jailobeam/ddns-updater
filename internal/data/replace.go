package data

import (
	"fmt"

	"github.com/qdm12/ddns-updater/internal/provider"
	"github.com/qdm12/ddns-updater/internal/records"
)

// ReplaceProviders replaces the configured providers while preserving their
// history from the persistent updates database.
func (db *Database) ReplaceProviders(providers []provider.Provider) (err error) {
	newData := make([]records.Record, len(providers))
	for i, configuredProvider := range providers {
		events, err := db.persistentDB.GetEvents(
			configuredProvider.Domain(),
			configuredProvider.Owner(),
			configuredProvider.IPVersion(),
		)
		if err != nil {
			return fmt.Errorf("reading history for %s: %w", configuredProvider, err)
		}
		newData[i] = records.New(configuredProvider, events)
	}

	db.Lock()
	db.data = newData
	db.Unlock()

	return nil
}
