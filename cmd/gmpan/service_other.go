//go:build !windows

package main

import (
	"context"
	"gmpan/internal/app"
	"log/slog"
	"net/http"
)

func serve(ctx context.Context, _ context.CancelFunc, server *http.Server, s *app.Server, c app.Config, l *slog.Logger) error {
	return runHTTP(ctx, server, s, c, l)
}
