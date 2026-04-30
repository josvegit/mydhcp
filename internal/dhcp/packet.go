package dhcp

import (
	"encoding/binary"
	"fmt"
	"net"
)

const (
	OpRequest byte = 1
	OpReply   byte = 2

	HTypeEthernet byte = 1
	HLenEthernet  byte = 6

	BroadcastFlag uint16 = 0x8000

	minPacketLen   = 236
	magicCookie0   = 99
	magicCookie1   = 130
	magicCookie2   = 83
	magicCookie3   = 99
)

// Packet represents a DHCP message.
type Packet struct {
	Op      byte
	HType   byte
	HLen    byte
	Hops    byte
	XID     uint32
	Secs    uint16
	Flags   uint16
	CIAddr  net.IP
	YIAddr  net.IP
	SIAddr  net.IP
	GIAddr  net.IP
	CHAddr  net.HardwareAddr
	SName   [64]byte
	File    [128]byte
	Options Options
}

func (p *Packet) BroadcastRequested() bool {
	return p.Flags&BroadcastFlag != 0
}

// ClientID returns Option 61 value if present, otherwise chaddr as lowercase hex.
func (p *Packet) ClientID() string {
	if id := p.Options.ClientID(); id != "" {
		return id
	}
	return macToHex(p.CHAddr)
}

func macToHex(hw net.HardwareAddr) string {
	if len(hw) == 0 {
		return ""
	}
	const hextable = "0123456789abcdef"
	buf := make([]byte, len(hw)*2)
	for i, b := range hw {
		buf[i*2] = hextable[b>>4]
		buf[i*2+1] = hextable[b&0xf]
	}
	return string(buf)
}

// MACNoDash returns the chaddr as lowercase hex without separators.
func MACNoDash(hw net.HardwareAddr) string {
	return macToHex(hw)
}

// Parse decodes a raw DHCP packet from a UDP payload.
func Parse(data []byte) (*Packet, error) {
	if len(data) < minPacketLen {
		return nil, fmt.Errorf("packet too short: %d bytes", len(data))
	}

	p := &Packet{}
	p.Op = data[0]
	p.HType = data[1]
	p.HLen = data[2]
	p.Hops = data[3]
	p.XID = binary.BigEndian.Uint32(data[4:8])
	p.Secs = binary.BigEndian.Uint16(data[8:10])
	p.Flags = binary.BigEndian.Uint16(data[10:12])
	p.CIAddr = cloneIP4(data[12:16])
	p.YIAddr = cloneIP4(data[16:20])
	p.SIAddr = cloneIP4(data[20:24])
	p.GIAddr = cloneIP4(data[24:28])

	hlen := min(int(p.HLen), 16)
	p.CHAddr = make(net.HardwareAddr, hlen)
	copy(p.CHAddr, data[28:28+hlen])

	copy(p.SName[:], data[44:108])
	copy(p.File[:], data[108:236])

	// Options
	if len(data) <= minPacketLen+4 {
		p.Options = make(Options)
		return p, nil
	}
	optData := data[minPacketLen:]
	if len(optData) < 4 {
		p.Options = make(Options)
		return p, nil
	}
	if optData[0] != magicCookie0 || optData[1] != magicCookie1 ||
		optData[2] != magicCookie2 || optData[3] != magicCookie3 {
		return nil, fmt.Errorf("invalid magic cookie")
	}

	var err error
	p.Options, err = ParseOptions(optData[4:])
	if err != nil {
		return nil, fmt.Errorf("options: %w", err)
	}

	return p, nil
}

// Serialize encodes a DHCP packet to bytes.
func (p *Packet) Serialize() []byte {
	buf := make([]byte, minPacketLen)
	buf[0] = p.Op
	buf[1] = p.HType
	buf[2] = p.HLen
	buf[3] = p.Hops
	binary.BigEndian.PutUint32(buf[4:8], p.XID)
	binary.BigEndian.PutUint16(buf[8:10], p.Secs)
	binary.BigEndian.PutUint16(buf[10:12], p.Flags)
	putIP4(buf[12:16], p.CIAddr)
	putIP4(buf[16:20], p.YIAddr)
	putIP4(buf[20:24], p.SIAddr)
	putIP4(buf[24:28], p.GIAddr)
	copy(buf[28:44], p.CHAddr)
	copy(buf[44:108], p.SName[:])
	copy(buf[108:236], p.File[:])
	buf = append(buf, p.Options.Serialize()...)
	return buf
}

func cloneIP4(b []byte) net.IP {
	ip := make(net.IP, 4)
	copy(ip, b[:4])
	return ip
}

func putIP4(dst []byte, ip net.IP) {
	if v := ip.To4(); v != nil {
		copy(dst, v)
	}
}
