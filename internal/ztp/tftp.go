package ztp

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
)

// TFTP opcodes (RFC 1350).
const (
	tftpRRQ   = 1
	tftpWRQ   = 2
	tftpDATA  = 3
	tftpACK   = 4
	tftpERROR = 5

	tftpBlockSize = 512
)

// TFTP error codes (RFC 1350 §5).
const (
	tftpErrUndefined = 0
	tftpErrNotFound  = 1
	tftpErrAccess    = 2
	tftpErrIllegal   = 4
)

// TFTPServer is an embedded read-only TFTP server for ZTP config delivery.
type TFTPServer struct {
	listen  string
	manager *Manager
	subnets subnetLookup
}

// subnetLookup is a minimal interface to get subnet info for template rendering.
type subnetLookup interface {
	ForPacket(giaddr, serverIP net.IP) (subnetInfo, bool)
}

type subnetInfo interface {
	GetMask() net.IPMask
	GetRouter() net.IP
}

// NewTFTPServer creates a TFTP server.
func NewTFTPServer(listen string, manager *Manager, subnets *tftpSubnetAdapter) *TFTPServer {
	return &TFTPServer{
		listen:  listen,
		manager: manager,
		subnets: subnets,
	}
}

// TFTPSubnetAdapter adapts subnet.Manager for the TFTP server.
type TFTPSubnetAdapter struct {
	mgr tftpSubnetManager
}

type tftpSubnetManager interface {
	ForPacketByIP(ip net.IP) (net.IPMask, net.IP, bool)
}

func (a *TFTPSubnetAdapter) ForPacket(giaddr, serverIP net.IP) (subnetInfo, bool) {
	return nil, false
}

type tftpSubnetAdapter = TFTPSubnetAdapter

// ServeTFTP listens for TFTP requests and serves ZTP configs.
func ServeTFTP(listen string, manager *Manager, getSubnetForIP func(net.IP) (net.IPMask, net.IP, bool)) error {
	addr, err := net.ResolveUDPAddr("udp4", listen)
	if err != nil {
		return fmt.Errorf("resolve TFTP listen addr: %w", err)
	}

	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("TFTP listen %s: %w", listen, err)
	}
	defer conn.Close()

	slog.Info("TFTP server listening", "addr", listen)

	buf := make([]byte, 65536)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			slog.Error("TFTP read error", "err", err)
			continue
		}
		go handleTFTPRequest(buf[:n], clientAddr, manager, getSubnetForIP)
	}
}

func handleTFTPRequest(data []byte, clientAddr *net.UDPAddr, manager *Manager, getSubnetForIP func(net.IP) (net.IPMask, net.IP, bool)) {
	if len(data) < 2 {
		return
	}
	opcode := binary.BigEndian.Uint16(data[:2])

	switch opcode {
	case tftpRRQ:
		handleRRQ(data[2:], clientAddr, manager, getSubnetForIP)
	case tftpWRQ:
		sendTFTPError(clientAddr, tftpErrAccess, "write requests not supported")
	default:
		sendTFTPError(clientAddr, tftpErrIllegal, "unexpected opcode")
	}
}

func handleRRQ(data []byte, clientAddr *net.UDPAddr, manager *Manager, getSubnetForIP func(net.IP) (net.IPMask, net.IP, bool)) {
	// Parse filename\0mode\0
	parts := strings.SplitN(string(data), "\x00", 3)
	if len(parts) < 2 {
		sendTFTPError(clientAddr, tftpErrIllegal, "malformed RRQ")
		return
	}
	filename := parts[0]

	// Expect filename like "aabbccddeeff.cfg"
	mac, err := parseMACFromFilename(filename)
	if err != nil {
		sendTFTPError(clientAddr, tftpErrUndefined, fmt.Sprintf("unparseable filename: %s", filename))
		return
	}

	device, profile, err := manager.LookupForTFTP(mac)
	if err != nil {
		if strings.Contains(err.Error(), "device not found") {
			sendTFTPError(clientAddr, tftpErrNotFound, "File Not Found")
		} else if strings.Contains(err.Error(), "dynamically-assigned") {
			sendTFTPError(clientAddr, tftpErrAccess, err.Error())
		} else {
			sendTFTPError(clientAddr, tftpErrNotFound, err.Error())
		}
		return
	}

	var mask net.IPMask
	var router net.IP
	if getSubnetForIP != nil && device.StaticIP != nil {
		m, r, ok := getSubnetForIP(device.StaticIP)
		if ok {
			mask = m
			router = r
		}
	}

	content, err := RenderConfig(*profile, *device, device.StaticIP, mask, router)
	if err != nil {
		sendTFTPError(clientAddr, tftpErrUndefined, fmt.Sprintf("template error: %s", err))
		return
	}

	if err := streamTFTPData(clientAddr, content); err != nil {
		slog.Error("TFTP stream error", "client", clientAddr, "file", filename, "err", err)
	}
}

func parseMACFromFilename(filename string) (net.HardwareAddr, error) {
	name := filename
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSuffix(name, ".cfg")
	if len(name) != 12 {
		return nil, fmt.Errorf("expected 12 hex chars, got %d", len(name))
	}

	// Insert colons: aabbccddeeff → aa:bb:cc:dd:ee:ff
	var buf [17]byte
	for i := range 6 {
		if i > 0 {
			buf[i*3-1] = ':'
		}
		buf[i*3] = name[i*2]
		buf[i*3+1] = name[i*2+1]
	}
	return net.ParseMAC(string(buf[:]))
}

func streamTFTPData(clientAddr *net.UDPAddr, data []byte) error {
	conn, err := net.DialUDP("udp4", nil, clientAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	block := uint16(1)
	sent := 0

	for {
		end := min(sent+tftpBlockSize, len(data))
		chunk := data[sent:end]

		pkt := make([]byte, 4+len(chunk))
		binary.BigEndian.PutUint16(pkt[0:2], tftpDATA)
		binary.BigEndian.PutUint16(pkt[2:4], block)
		copy(pkt[4:], chunk)

		if _, err := conn.Write(pkt); err != nil {
			return fmt.Errorf("send DATA block %d: %w", block, err)
		}

		ack := make([]byte, 4)
		if _, err := conn.Read(ack); err != nil {
			return fmt.Errorf("read ACK block %d: %w", block, err)
		}
		if binary.BigEndian.Uint16(ack[0:2]) != tftpACK {
			return fmt.Errorf("expected ACK, got opcode %d", binary.BigEndian.Uint16(ack[0:2]))
		}
		if binary.BigEndian.Uint16(ack[2:4]) != block {
			return fmt.Errorf("ACK block mismatch: got %d, want %d", binary.BigEndian.Uint16(ack[2:4]), block)
		}

		sent = end
		if len(chunk) < tftpBlockSize {
			break
		}
		block++
	}
	return nil
}

func sendTFTPError(clientAddr *net.UDPAddr, code uint16, msg string) {
	conn, err := net.DialUDP("udp4", nil, clientAddr)
	if err != nil {
		return
	}
	defer conn.Close()

	pkt := make([]byte, 4+len(msg)+1)
	binary.BigEndian.PutUint16(pkt[0:2], tftpERROR)
	binary.BigEndian.PutUint16(pkt[2:4], code)
	copy(pkt[4:], msg)
	pkt[len(pkt)-1] = 0
	conn.Write(pkt) //nolint:errcheck
}
