package ztp

import (
	"errors"
	"net"
	"sync"
)

var (
	ErrDeviceNotFound  = errors.New("device not found")
	ErrProfileNotFound = errors.New("profile not found")
	ErrDeviceExists    = errors.New("device already exists")
	ErrProfileExists   = errors.New("profile already exists")
)

// MemoryDeviceStore is an in-memory implementation of DeviceStore.
type MemoryDeviceStore struct {
	mu      sync.RWMutex
	devices map[string]DeviceRecord // key: MACKey
}

func NewMemoryDeviceStore() *MemoryDeviceStore {
	return &MemoryDeviceStore{devices: make(map[string]DeviceRecord)}
}

func (s *MemoryDeviceStore) Add(d DeviceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[MACKey(d.MAC)] = d
	return nil
}

func (s *MemoryDeviceStore) Get(mac net.HardwareAddr) (DeviceRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[MACKey(mac)]
	return d, ok
}

func (s *MemoryDeviceStore) Delete(mac net.HardwareAddr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := MACKey(mac)
	_, ok := s.devices[key]
	if ok {
		delete(s.devices, key)
	}
	return ok
}

func (s *MemoryDeviceStore) List() []DeviceRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]DeviceRecord, 0, len(s.devices))
	for _, d := range s.devices {
		list = append(list, d)
	}
	return list
}

// MemoryProfileStore is an in-memory implementation of ProfileStore.
type MemoryProfileStore struct {
	mu       sync.RWMutex
	profiles map[string]VendorProfile // key: Name
}

func NewMemoryProfileStore() *MemoryProfileStore {
	return &MemoryProfileStore{profiles: make(map[string]VendorProfile)}
}

func (s *MemoryProfileStore) Add(p VendorProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[p.Name] = p
	return nil
}

func (s *MemoryProfileStore) Get(name string) (VendorProfile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[name]
	return p, ok
}

func (s *MemoryProfileStore) GetByOption60(option60 string) (VendorProfile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if option60 == "" {
		return VendorProfile{}, false
	}
	for _, p := range s.profiles {
		if p.MatchOption60 != "" && p.MatchOption60 == option60 {
			return p, true
		}
	}
	return VendorProfile{}, false
}

func (s *MemoryProfileStore) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.profiles[name]
	if ok {
		delete(s.profiles, name)
	}
	return ok
}

func (s *MemoryProfileStore) List() []VendorProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]VendorProfile, 0, len(s.profiles))
	for _, p := range s.profiles {
		list = append(list, p)
	}
	return list
}
