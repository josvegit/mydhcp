package auditlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/josvegit/mydhcp/internal/plugin"
)

// AuditLog is a plugin that writes lease events as JSON lines to a file.
type AuditLog struct {
	plugin.BasePlugin
	mu   sync.Mutex
	file *os.File
}

type entry struct {
	Event      string `json:"event"`
	IP         string `json:"ip"`
	ClientID   string `json:"client_id"`
	ClientHW   string `json:"client_hw,omitempty"`
	SubnetName string `json:"subnet"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

func New(path string) (*AuditLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("auditlog: open %s: %w", path, err)
	}
	return &AuditLog{file: f}, nil
}

func (a *AuditLog) Name() string { return "auditlog" }

func (a *AuditLog) OnLeaseEvent(ev plugin.LeaseEvent) error {
	e := entry{
		Event:      string(ev.Type),
		IP:         ev.IP.String(),
		ClientID:   ev.ClientID,
		SubnetName: ev.SubnetName,
	}
	if len(ev.ClientHW) > 0 {
		e.ClientHW = ev.ClientHW.String()
	}
	if !ev.ExpiresAt.IsZero() {
		e.ExpiresAt = ev.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
	}

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("auditlog: marshal: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	_, err = fmt.Fprintf(a.file, "%s\n", line)
	return err
}

func (a *AuditLog) OnShutdown(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.file.Close()
}
