package plugin

import (
	"context"
	"net"
	"time"
)

type LeaseEventType string

const (
	EventAssigned LeaseEventType = "assigned"
	EventRenewed  LeaseEventType = "renewed"
	EventReleased LeaseEventType = "released"
	EventExpired  LeaseEventType = "expired"
	EventDeclined LeaseEventType = "declined"
)

// LeaseEvent carries information about a lease state change.
// Uses only stdlib types to avoid circular imports.
type LeaseEvent struct {
	Type       LeaseEventType
	IP         net.IP
	ClientID   string
	ClientHW   net.HardwareAddr
	GiAddr     net.IP
	SubnetName string
	SubnetCIDR string
	ExpiresAt  time.Time
}

type Plugin interface {
	Name() string
	OnLeaseEvent(event LeaseEvent) error
	OnShutdown(ctx context.Context) error
}

// BasePlugin provides no-op implementations for embedding.
type BasePlugin struct{}

func (BasePlugin) OnLeaseEvent(LeaseEvent) error          { return nil }
func (BasePlugin) OnShutdown(context.Context) error       { return nil }
