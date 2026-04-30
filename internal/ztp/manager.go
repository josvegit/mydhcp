package ztp

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/josvegit/mydhcp/internal/subnet"
)

var (
	ErrNoSubnet   = errors.New("no subnet found for this IP")
	ErrOutOfRange = errors.New("IP is outside the subnet's configured range")
)

// Manager is the single entry point for all ZTP operations.
type Manager struct {
	mu       sync.RWMutex
	devices  DeviceStore
	profiles ProfileStore
	subnets  *subnet.Manager
}

func NewManager(subnets *subnet.Manager) *Manager {
	return &Manager{
		devices:  NewMemoryDeviceStore(),
		profiles: NewMemoryProfileStore(),
		subnets:  subnets,
	}
}

// LookupForPacket finds the device record and vendor profile for an incoming DHCP packet.
func (m *Manager) LookupForPacket(chaddr net.HardwareAddr, option60 string) (*DeviceRecord, *VendorProfile) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var device *DeviceRecord
	var profile *VendorProfile

	if d, ok := m.devices.Get(chaddr); ok {
		d2 := d
		device = &d2
	}

	if device != nil && device.VendorProfile != "" {
		if p, ok := m.profiles.Get(device.VendorProfile); ok {
			p2 := p
			profile = &p2
		}
	} else if option60 != "" {
		if p, ok := m.profiles.GetByOption60(option60); ok {
			p2 := p
			profile = &p2
		}
	}

	return device, profile
}

// LookupForTFTP finds the device record and vendor profile for a TFTP request.
func (m *Manager) LookupForTFTP(mac net.HardwareAddr) (*DeviceRecord, *VendorProfile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.devices.Get(mac)
	if !ok {
		return nil, nil, fmt.Errorf("device not found")
	}
	if d.StaticIP == nil {
		return nil, nil, fmt.Errorf("config not available for dynamically-assigned devices")
	}
	if d.VendorProfile == "" {
		return &d, nil, fmt.Errorf("device has no vendor profile assigned")
	}
	p, ok := m.profiles.Get(d.VendorProfile)
	if !ok {
		return &d, nil, fmt.Errorf("vendor profile %q not found", d.VendorProfile)
	}
	return &d, &p, nil
}

// AddDevice registers a device and reserves its static IP in the appropriate lease store.
func (m *Manager) AddDevice(record DeviceRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if record.VendorProfile != "" {
		if _, ok := m.profiles.Get(record.VendorProfile); !ok {
			return fmt.Errorf("vendor profile %q does not exist", record.VendorProfile)
		}
	}

	if record.StaticIP != nil {
		_, leaseStore, ok := m.subnets.SubnetForIP(record.StaticIP)
		if !ok {
			return ErrNoSubnet
		}

		if err := leaseStore.Reserve(record.ClientID(), record.StaticIP); err != nil {
			return fmt.Errorf("reserve IP %s: %w", record.StaticIP, err)
		}

		if err := m.devices.Add(record); err != nil {
			_ = leaseStore.Unreserve(record.StaticIP)
			return fmt.Errorf("add device: %w", err)
		}
		return nil
	}

	return m.devices.Add(record)
}

// DeleteDevice removes a device record and unreserves its static IP.
func (m *Manager) DeleteDevice(mac net.HardwareAddr) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.devices.Get(mac)
	if !ok {
		return ErrDeviceNotFound
	}

	if record.StaticIP != nil {
		_, leaseStore, found := m.subnets.SubnetForIP(record.StaticIP)
		if found {
			if err := leaseStore.Unreserve(record.StaticIP); err != nil {
				return fmt.Errorf("unreserve IP %s: %w", record.StaticIP, err)
			}
		}
	}

	m.devices.Delete(mac)
	return nil
}

// AddProfile registers a vendor profile.
func (m *Manager) AddProfile(profile VendorProfile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.profiles.Add(profile)
}

// DeleteProfile removes a vendor profile and returns MACs of affected devices.
func (m *Manager) DeleteProfile(name string) (affectedMACs []string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.profiles.Get(name); !ok {
		return nil, ErrProfileNotFound
	}

	for _, d := range m.devices.List() {
		if d.VendorProfile == name {
			affectedMACs = append(affectedMACs, d.MAC.String())
		}
	}

	m.profiles.Delete(name)
	return affectedMACs, nil
}

// GetDevice returns a device record by MAC.
func (m *Manager) GetDevice(mac net.HardwareAddr) (DeviceRecord, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.devices.Get(mac)
}

// ListDevices returns all device records.
func (m *Manager) ListDevices() []DeviceRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.devices.List()
}

// GetProfile returns a vendor profile by name.
func (m *Manager) GetProfile(name string) (VendorProfile, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.profiles.Get(name)
}

// ListProfiles returns all vendor profiles.
func (m *Manager) ListProfiles() []VendorProfile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.profiles.List()
}
