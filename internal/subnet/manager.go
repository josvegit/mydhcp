package subnet

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/josvegit/mydhcp/internal/lease"
	"github.com/josvegit/mydhcp/internal/store"
)

var (
	ErrNotFound      = errors.New("subnet not found")
	ErrAlreadyExists = errors.New("subnet already exists")
	ErrOverlap       = errors.New("subnet CIDR overlaps an existing subnet")
	ErrHasOccupied   = errors.New("subnet has occupied leases")
)

// Config holds the runtime subnet configuration.
type Config struct {
	Name                string
	Network             *net.IPNet
	Router              net.IP
	DNS                 []net.IP
	BroadcastAddr       net.IP
	LeaseTime           time.Duration
	OfferTimeout        time.Duration
	DeclineCooldown     time.Duration
	LeaseReaperInterval time.Duration
	RangeStart          net.IP
	RangeEnd            net.IP
}

type entry struct {
	cfg Config
	mem *store.MemoryStore
}

// Manager holds the set of active subnets and their lease stores.
type Manager struct {
	mu      sync.RWMutex
	subnets map[string]*entry
}

func NewManager() *Manager {
	return &Manager{subnets: make(map[string]*entry)}
}

func (m *Manager) Add(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.subnets[cfg.Name]; ok {
		return ErrAlreadyExists
	}

	for _, e := range m.subnets {
		if overlaps(e.cfg.Network, cfg.Network) {
			return fmt.Errorf("%w: %s overlaps %s", ErrOverlap, cfg.Network, e.cfg.Network)
		}
	}

	ms := store.NewMemoryStore(
		cfg.RangeStart, cfg.RangeEnd,
		cfg.LeaseTime, cfg.OfferTimeout, cfg.DeclineCooldown,
	)
	m.subnets[cfg.Name] = &entry{cfg: cfg, mem: ms}
	return nil
}

func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.subnets[name]
	if !ok {
		return ErrNotFound
	}
	if e.mem.OccupiedCount() > 0 {
		return ErrHasOccupied
	}
	delete(m.subnets, name)
	return nil
}

// ForPacket returns the subnet config and lease store matching the given lookup IP.
// If giaddr is zero/nil, serverIP is used for direct-connect matching.
func (m *Manager) ForPacket(giaddr, serverIP net.IP) (Config, lease.Store, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lookup := giaddr
	if lookup == nil || lookup.IsUnspecified() {
		lookup = serverIP
	}

	for _, e := range m.subnets {
		if e.cfg.Network.Contains(lookup) {
			return e.cfg, e.mem, true
		}
	}
	return Config{}, nil, false
}

func (m *Manager) Get(name string) (Config, lease.Store, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, ok := m.subnets[name]
	if !ok {
		return Config{}, nil, false
	}
	return e.cfg, e.mem, true
}

func (m *Manager) List() []Config {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cfgs := make([]Config, 0, len(m.subnets))
	for _, e := range m.subnets {
		cfgs = append(cfgs, e.cfg)
	}
	return cfgs
}

// GetStore returns the MemoryStore for a subnet (for API breakdown and reaper).
func (m *Manager) GetStore(name string) (*store.MemoryStore, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, ok := m.subnets[name]
	if !ok {
		return nil, false
	}
	return e.mem, true
}

// GetLeaseStore returns the lease.Store for a subnet by name.
func (m *Manager) GetLeaseStore(name string) (lease.Store, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, ok := m.subnets[name]
	if !ok {
		return nil, false
	}
	return e.mem, true
}

// SubnetForIP returns the subnet containing a given IP (used by ZTP).
func (m *Manager) SubnetForIP(ip net.IP) (Config, lease.Store, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, e := range m.subnets {
		if e.cfg.Network.Contains(ip) {
			return e.cfg, e.mem, true
		}
	}
	return Config{}, nil, false
}

func overlaps(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// BroadcastAddr computes the broadcast address for a network.
func BroadcastAddr(network *net.IPNet) net.IP {
	ip := network.IP.To4()
	if ip == nil {
		return nil
	}
	mask := network.Mask
	bcast := make(net.IP, 4)
	for i := range 4 {
		bcast[i] = ip[i] | ^mask[i]
	}
	return bcast
}
