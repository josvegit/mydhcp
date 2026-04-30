// Package dhcp re-exports lease types for convenience.
package dhcp

import "github.com/josvegit/mydhcp/internal/lease"

// Type aliases so existing code using dhcp.Lease etc. still compiles.
type (
	Lease      = lease.Lease
	LeaseState = lease.State
	LeaseStore = lease.Store
)

const (
	StateReserved = lease.StateReserved
	StateOffered  = lease.StateOffered
	StateBound    = lease.StateBound
	StateExpired  = lease.StateExpired
	StateDeclined = lease.StateDeclined
)
