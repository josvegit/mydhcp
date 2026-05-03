package dhcp

import (
	"encoding/binary"
	"fmt"
	"net"
)

// DHCP option codes.
const (
	OptSubnetMask       byte = 1
	OptRouter           byte = 3
	OptDNS              byte = 6
	OptBroadcastAddress byte = 28
	OptRequestedIP      byte = 50
	OptLeaseTime        byte = 51
	OptMsgType          byte = 53
	OptServerID         byte = 54
	OptParameterList    byte = 55
	OptVendorClass      byte = 60
	OptClientID         byte = 61
	OptTFTPServer       byte = 66
	OptBootfile         byte = 67
	OptEnd              byte = 255
)

// DHCP message type values (option 53).
const (
	MsgDiscover byte = 1
	MsgOffer    byte = 2
	MsgRequest  byte = 3
	MsgDecline  byte = 4
	MsgAck      byte = 5
	MsgNak      byte = 6
	MsgRelease  byte = 7
	MsgInform   byte = 8
)

// Options is a map of option code to raw value bytes.
type Options map[byte][]byte

// ParseOptions decodes the options field starting after the magic cookie.
func ParseOptions(data []byte) (Options, error) {
	opts := make(Options)
	i := 0
	for i < len(data) {
		code := data[i]
		i++
		if code == 0 {
			continue
		}
		if code == OptEnd {
			break
		}
		if i >= len(data) {
			return nil, fmt.Errorf("option %d: missing length", code)
		}
		l := int(data[i])
		i++
		if i+l > len(data) {
			return nil, fmt.Errorf("option %d: length %d overflows buffer", code, l)
		}
		opts[code] = data[i : i+l]
		i += l
	}
	return opts, nil
}

// Serialize encodes options into a byte slice with the magic cookie prefix and End option.
func (o Options) Serialize() []byte {
	var buf []byte
	// Magic cookie
	buf = append(buf, 99, 130, 83, 99)
	for code, val := range o {
		buf = append(buf, code, byte(len(val)))
		buf = append(buf, val...)
	}
	buf = append(buf, OptEnd)
	return buf
}

func (o Options) MsgType() (byte, bool) {
	v, ok := o[OptMsgType]
	if !ok || len(v) < 1 {
		return 0, false
	}
	return v[0], true
}

func (o Options) ServerID() net.IP {
	v, ok := o[OptServerID]
	if !ok || len(v) < 4 {
		return nil
	}
	return net.IP(v).To4()
}

func (o Options) RequestedIP() net.IP {
	v, ok := o[OptRequestedIP]
	if !ok || len(v) < 4 {
		return nil
	}
	return net.IP(v).To4()
}

func (o Options) ClientID() string {
	v, ok := o[OptClientID]
	if !ok || len(v) == 0 {
		return ""
	}
	// RFC 2132 §9.14: first byte is type; 0x01 = Ethernet MAC (6 bytes follow).
	if len(v) == 7 && v[0] == 0x01 {
		return net.HardwareAddr(v[1:]).String()
	}
	return fmt.Sprintf("%x", v)
}

func (o Options) VendorClass() string {
	v, ok := o[OptVendorClass]
	if !ok {
		return ""
	}
	return string(v)
}

func EncodeIP(ip net.IP) []byte {
	v := ip.To4()
	if v == nil {
		return []byte{0, 0, 0, 0}
	}
	return []byte(v)
}

func EncodeIPs(ips []net.IP) []byte {
	var b []byte
	for _, ip := range ips {
		b = append(b, EncodeIP(ip)...)
	}
	return b
}

func EncodeUint32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
