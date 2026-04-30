package ztp

import (
	"fmt"
	"net"
)

// DeviceRecord represents a known network device with optional ZTP configuration.
type DeviceRecord struct {
	MAC           net.HardwareAddr
	VendorProfile string
	StaticIP      net.IP
	Hostname      string
	Vars          map[string]any
}

// ClientID returns the MAC address as a lowercase hex string.
func (d DeviceRecord) ClientID() string {
	if len(d.MAC) == 0 {
		return ""
	}
	const hextable = "0123456789abcdef"
	buf := make([]byte, len(d.MAC)*2)
	for i, b := range d.MAC {
		buf[i*2] = hextable[b>>4]
		buf[i*2+1] = hextable[b&0xf]
	}
	return string(buf)
}

// MACNoDash returns the MAC address without separators, lowercase.
func (d DeviceRecord) MACNoDash() string {
	return d.ClientID()
}

// DeviceStore manages device records.
type DeviceStore interface {
	Add(DeviceRecord) error
	Get(mac net.HardwareAddr) (DeviceRecord, bool)
	Delete(mac net.HardwareAddr) bool
	List() []DeviceRecord
}

// ParseMAC parses a MAC address string in any common format.
func ParseMAC(s string) (net.HardwareAddr, error) {
	hw, err := net.ParseMAC(s)
	if err != nil {
		return nil, fmt.Errorf("invalid MAC address %q: %w", s, err)
	}
	return hw, nil
}

// MACKey returns a canonical string key for a MAC address.
func MACKey(mac net.HardwareAddr) string {
	if len(mac) == 0 {
		return ""
	}
	const hextable = "0123456789abcdef"
	buf := make([]byte, len(mac)*2)
	for i, b := range mac {
		buf[i*2] = hextable[b>>4]
		buf[i*2+1] = hextable[b&0xf]
	}
	return string(buf)
}
