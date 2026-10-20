//go:build windows

package main

import (
	"context"
	"gmpan/internal/app"
	"golang.org/x/sys/windows/svc"
	"log/slog"
	"net/http"
)

type windowsService struct {
	ctx    context.Context
	cancel context.CancelFunc
	server *http.Server
	s      *app.Server
	c      app.Config
	l      *slog.Logger
}

func (v *windowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	go func() { done <- runHTTP(v.ctx, v.server, v.s, v.c, v.l) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				v.cancel()
				if e := <-done; e != nil {
					v.l.Error("service shutdown failed")
					return false, 1
				}
				return false, 0
			}
		case e := <-done:
			if e != nil {
				v.l.Error("service stopped", "error", e.Error())
				return false, 1
			}
			return false, 0
		}
	}
}
func serve(ctx context.Context, cancel context.CancelFunc, server *http.Server, s *app.Server, c app.Config, l *slog.Logger) error {
	isService, e := svc.IsWindowsService()
	if e != nil {
		return e
	}
	if !isService {
		return runHTTP(ctx, server, s, c, l)
	}
	return svc.Run("GmPan", &windowsService{ctx, cancel, server, s, c, l})
}
