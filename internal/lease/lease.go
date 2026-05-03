// Package lease defines the Lease types and LeaseStore interface.
// It has no internal dependencies so that subnet, store, ztp, and dhcp
// can all import it without creating import cycles.
package lease

import (
	"net"
	"time"
)

type State int

const (
	StateReserved State = iota
	StateOffered
	StateBound
	StateExpired
	StateDeclined
)

func (s State) String() string {
	switch s {
	case StateReserved:
		return "reserved"
	case StateOffered:
		return "offered"
	case StateBound:
		return "bound"
	case StateExpired:
		return "expired"
	case StateDeclined:
		return "declined"
	default:
		return "unknown"
	}
}

type Lease struct {
	IP         net.IP
	ClientID   string
	GiAddr     net.IP
	State      State
	OfferedAt  time.Time
	BoundAt    time.Time
	ExpiresAt  time.Time
	DeclinedAt time.Time
}

// Store manages IP leases for a single subnet.
type Store interface {
	Reserve(clientID string, ip net.IP) error
	Unreserve(ip net.IP) error
	Allocate(clientID string, ip net.IP) (Lease, error)
	Renew(clientID string, ip net.IP) (Lease, error)
	Release(clientID string, ip net.IP) error
	Decline(ip net.IP) error
	Get(ip net.IP) (Lease, bool)
	GetByClient(clientID string) (Lease, bool)
	NextAvailable() (net.IP, bool)
	OccupiedCount() int
	List() []Lease
	Expired() []Lease
	Declined() []Lease
}
