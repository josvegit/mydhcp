package plugin

import (
	"context"
	"log/slog"
)

type Registry struct {
	plugins []Plugin
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Register(p Plugin) {
	r.plugins = append(r.plugins, p)
}

func (r *Registry) EmitLeaseEvent(event LeaseEvent) {
	for _, p := range r.plugins {
		go func() {
			if err := p.OnLeaseEvent(event); err != nil {
				slog.Error("plugin lease event error", "plugin", p.Name(), "err", err)
			}
		}()
	}
}

func (r *Registry) Shutdown(ctx context.Context) {
	for i := len(r.plugins) - 1; i >= 0; i-- {
		if err := r.plugins[i].OnShutdown(ctx); err != nil {
			slog.Error("plugin shutdown error", "plugin", r.plugins[i].Name(), "err", err)
		}
	}
}
