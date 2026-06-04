package server

import (
	"context"

	"github.com/qdm12/goservices/httpserver"
)

func New(ctx context.Context, address, rootURL, configPath string, db Database,
	logger Logger, runner UpdateForcer,
) (server *httpserver.Server, err error) {
	return httpserver.New(httpserver.Settings{
		Handler: newHandler(ctx, rootURL, configPath, db, runner),
		Address: &address,
		Logger:  logger,
	})
}
