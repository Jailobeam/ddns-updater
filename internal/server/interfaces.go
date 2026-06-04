package server

import (
	"context"

	"github.com/qdm12/ddns-updater/internal/provider"
	"github.com/qdm12/ddns-updater/internal/records"
)

type Database interface {
	SelectAll() (records []records.Record)
	ReplaceProviders(providers []provider.Provider) (err error)
}

type UpdateForcer interface {
	ForceUpdate(ctx context.Context) (errors []error)
}

type Logger interface {
	Info(s string)
	Warn(s string)
	Error(s string)
}
