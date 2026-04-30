package store

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/josvegit/mydhcp/internal/lease"
)

var (
	ErrAlreadyReserved = errors.New("IP already reserved for a different client")
	ErrNotReserved     = errors.New("IP is not reserved")
	ErrNotFound        = errors.New("lease not found")
	ErrIPUnavailable   = errors.New("IP is not available for allocation")
	ErrClientMismatch  = errors.New("client ID does not match existing lease")
)

// MemoryStore is an in-memory implementation of lease.Store.
type MemoryStore struct {
	mu         sync.Mutex
	byIP       map[string]*lease.Lease
	byClient   map[string]*lease.Lease
	available  []net.IP
	leaseTime  time.Duration
	offerTTL   time.Duration
	declineTTL time.Duration
}

func NewMemoryStore(start, end net.IP, leaseTime, offerTTL, declineTTL time.Duration) *MemoryStore {
	s := &MemoryStore{
		byIP:       make(map[string]*lease.Lease),
		byClient:   make(map[string]*lease.Lease),
		leaseTime:  leaseTime,
		offerTTL:   offerTTL,
		declineTTL: declineTTL,
	}

	start4 := start.To4()
	end4 := end.To4()
	if start4 == nil || end4 == nil {
		return s
	}

	cur := ipToUint32(start4)
	last := ipToUint32(end4)
	for cur <= last {
		ip := make(net.IP, 4)
		uint32ToIP(ip, cur)
		s.available = append(s.available, ip)
		cur++
	}
	return s
}

func (s *MemoryStore) Reserve(clientID string, ip net.IP) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := ip.String()
	if existing, ok := s.byIP[key]; ok {
		if existing.ClientID != clientID {
			return ErrAlreadyReserved
		}
		return nil
	}

	s.available = removeIP(s.available, ip)

	l := &lease.Lease{
		IP:       cloneIP(ip),
		ClientID: clientID,
		State:    lease.StateReserved,
	}
	s.byIP[key] = l
	s.byClient[clientID] = l
	return nil
}

func (s *MemoryStore) Unreserve(ip net.IP) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := ip.String()
	l, ok := s.byIP[key]
	if !ok {
		return ErrNotReserved
	}
	if l.State != lease.StateReserved {
		return fmt.Errorf("lease for %s is in state %s, not reserved", ip, l.State)
	}

	delete(s.byIP, key)
	if s.byClient[l.ClientID] == l {
		delete(s.byClient, l.ClientID)
	}

	s.available = append(s.available, cloneIP(ip))
	return nil
}

func (s *MemoryStore) Allocate(clientID string, ip net.IP) (lease.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := ip.String()
	if existing, ok := s.byIP[key]; ok {
		switch existing.State {
		case lease.StateReserved:
			if existing.ClientID != clientID {
				return lease.Lease{}, ErrAlreadyReserved
			}
			existing.State = lease.StateOffered
			existing.OfferedAt = time.Now()
			existing.ExpiresAt = time.Now().Add(s.offerTTL)
			if s.byClient[clientID] != existing {
				if old := s.byClient[clientID]; old != nil {
					old.ClientID = ""
				}
				s.byClient[clientID] = existing
			}
			return *existing, nil
		case lease.StateOffered:
			if existing.ClientID == clientID {
				existing.OfferedAt = time.Now()
				existing.ExpiresAt = time.Now().Add(s.offerTTL)
				return *existing, nil
			}
			return lease.Lease{}, ErrIPUnavailable
		default:
			return lease.Lease{}, ErrIPUnavailable
		}
	}

	s.available = removeIP(s.available, ip)

	now := time.Now()
	l := &lease.Lease{
		IP:        cloneIP(ip),
		ClientID:  clientID,
		State:     lease.StateOffered,
		OfferedAt: now,
		ExpiresAt: now.Add(s.offerTTL),
	}
	s.byIP[key] = l
	if old := s.byClient[clientID]; old != nil && old != l {
		delete(s.byIP, old.IP.String())
	}
	s.byClient[clientID] = l
	return *l, nil
}

func (s *MemoryStore) Renew(clientID string, ip net.IP) (lease.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := ip.String()
	l, ok := s.byIP[key]
	if !ok {
		return lease.Lease{}, ErrNotFound
	}
	if l.ClientID != clientID {
		return lease.Lease{}, ErrClientMismatch
	}

	now := time.Now()
	switch l.State {
	case lease.StateOffered, lease.StateBound:
		l.State = lease.StateBound
		l.BoundAt = now
		l.ExpiresAt = now.Add(s.leaseTime)
		return *l, nil
	default:
		return lease.Lease{}, fmt.Errorf("cannot renew lease in state %s", l.State)
	}
}

func (s *MemoryStore) Release(clientID string, ip net.IP) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := ip.String()
	l, ok := s.byIP[key]
	if !ok {
		return ErrNotFound
	}
	if l.ClientID != clientID {
		return ErrClientMismatch
	}

	delete(s.byIP, key)
	if s.byClient[clientID] == l {
		delete(s.byClient, clientID)
	}
	s.available = append(s.available, cloneIP(ip))
	return nil
}

func (s *MemoryStore) Decline(ip net.IP) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := ip.String()
	l, ok := s.byIP[key]
	if !ok {
		return ErrNotFound
	}

	l.State = lease.StateDeclined
	l.DeclinedAt = time.Now()
	return nil
}

func (s *MemoryStore) Get(ip net.IP) (lease.Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, ok := s.byIP[ip.String()]
	if !ok {
		return lease.Lease{}, false
	}
	return *l, true
}

func (s *MemoryStore) GetByClient(clientID string) (lease.Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, ok := s.byClient[clientID]
	if !ok {
		return lease.Lease{}, false
	}
	return *l, true
}

func (s *MemoryStore) NextAvailable() (net.IP, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.available) == 0 {
		return nil, false
	}
	// Pick from front (FIFO). The caller must call Allocate immediately.
	// If not allocated, the IP remains at the back; it will be offered again next time.
	ip := s.available[0]
	s.available = s.available[1:]
	s.available = append(s.available, cloneIP(ip))
	return cloneIP(ip), true
}

func (s *MemoryStore) OccupiedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, l := range s.byIP {
		switch l.State {
		case lease.StateReserved, lease.StateOffered, lease.StateBound:
			count++
		}
	}
	return count
}

func (s *MemoryStore) List() []lease.Lease {
	s.mu.Lock()
	defer s.mu.Unlock()

	leases := make([]lease.Lease, 0, len(s.byIP))
	for _, l := range s.byIP {
		leases = append(leases, *l)
	}
	return leases
}

// OccupiedByState returns counts of Reserved, Offered, and Bound leases for the API 409 response.
func (s *MemoryStore) OccupiedByState() (reserved, offered, bound int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, l := range s.byIP {
		switch l.State {
		case lease.StateReserved:
			reserved++
		case lease.StateOffered:
			offered++
		case lease.StateBound:
			bound++
		}
	}
	return
}

// Expired transitions expired leases and returns them to available.
func (s *MemoryStore) Expired() []lease.Lease {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var expired []lease.Lease

	for key, l := range s.byIP {
		if l.State != lease.StateOffered && l.State != lease.StateBound {
			continue
		}
		if l.ExpiresAt.IsZero() || !now.After(l.ExpiresAt) {
			continue
		}
		expired = append(expired, *l)
		delete(s.byIP, key)
		if s.byClient[l.ClientID] == l {
			delete(s.byClient, l.ClientID)
		}
		s.available = append(s.available, cloneIP(l.IP))
	}
	return expired
}

// Declined transitions leases past their cooldown and returns them to available.
func (s *MemoryStore) Declined() []lease.Lease {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var declined []lease.Lease

	for key, l := range s.byIP {
		if l.State != lease.StateDeclined {
			continue
		}
		if !now.After(l.DeclinedAt.Add(s.declineTTL)) {
			continue
		}
		declined = append(declined, *l)
		delete(s.byIP, key)
		if s.byClient[l.ClientID] == l {
			delete(s.byClient, l.ClientID)
		}
		s.available = append(s.available, cloneIP(l.IP))
	}
	return declined
}

func cloneIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	c := make(net.IP, len(ip))
	copy(c, ip)
	return c
}

func removeIP(slice []net.IP, ip net.IP) []net.IP {
	for i, v := range slice {
		if v.Equal(ip) {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func ipToUint32(ip net.IP) uint32 {
	ip4 := ip.To4()
	return uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
}

func uint32ToIP(ip net.IP, v uint32) {
	ip[0] = byte(v >> 24)
	ip[1] = byte(v >> 16)
	ip[2] = byte(v >> 8)
	ip[3] = byte(v)
}
