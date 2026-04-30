package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/josvegit/mydhcp/internal/subnet"
	"github.com/josvegit/mydhcp/internal/ztp"
)

// Server is the HTTP management API server.
type Server struct {
	listen  string
	subnets *subnet.Manager
	ztpMgr  *ztp.Manager
	srv     *http.Server
}

func NewServer(listen string, subnets *subnet.Manager, ztpMgr *ztp.Manager) *Server {
	s := &Server{
		listen:  listen,
		subnets: subnets,
		ztpMgr:  ztpMgr,
	}

	mux := http.NewServeMux()
	h := &handlers{subnets: subnets, ztpMgr: ztpMgr}

	mux.HandleFunc("GET /subnets", h.listSubnets)
	mux.HandleFunc("POST /subnets", h.addSubnet)
	mux.HandleFunc("DELETE /subnets/{name}", h.deleteSubnet)
	mux.HandleFunc("GET /leases", h.listLeases)

	mux.HandleFunc("GET /devices", h.listDevices)
	mux.HandleFunc("POST /devices", h.addDevice)
	mux.HandleFunc("GET /devices/{mac}", h.getDevice)
	mux.HandleFunc("DELETE /devices/{mac}", h.deleteDevice)

	mux.HandleFunc("GET /vendor-profiles", h.listProfiles)
	mux.HandleFunc("POST /vendor-profiles", h.addProfile)
	mux.HandleFunc("GET /vendor-profiles/{name}", h.getProfile)
	mux.HandleFunc("DELETE /vendor-profiles/{name}", h.deleteProfile)

	mux.HandleFunc("GET /configs/{mac}", h.getConfig)

	s.srv = &http.Server{
		Addr:    listen,
		Handler: mux,
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	slog.Info("API server listening", "addr", s.listen)

	go func() {
		<-ctx.Done()
		s.srv.Shutdown(context.Background()) //nolint:errcheck
	}()

	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("API server: %w", err)
	}
	return nil
}
