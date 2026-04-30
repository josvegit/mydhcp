package dhcp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"syscall"
	"time"

	"github.com/josvegit/mydhcp/internal/lease"
	"github.com/josvegit/mydhcp/internal/plugin"
	"github.com/josvegit/mydhcp/internal/subnet"
	"github.com/josvegit/mydhcp/internal/ztp"
)

// Server is the UDP DHCP server.
type Server struct {
	listen   string
	serverIP net.IP
	subnets  *subnet.Manager
	ztpMgr   *ztp.Manager
	plugins  *plugin.Registry
	handler  *Handler
	reaper   *Reaper
}

// ServerConfig holds the server startup configuration.
type ServerConfig struct {
	Listen   string
	ServerIP net.IP
}

func NewServer(cfg ServerConfig, subnets *subnet.Manager, ztpMgr *ztp.Manager, plugins *plugin.Registry) *Server {
	return &Server{
		listen:   cfg.Listen,
		serverIP: cfg.ServerIP,
		subnets:  subnets,
		ztpMgr:   ztpMgr,
		plugins:  plugins,
		handler:  NewHandler(cfg.ServerIP, plugins),
		reaper:   NewReaper(plugins),
	}
}

// Run starts the DHCP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	conn, err := listenDHCP(s.listen)
	if err != nil {
		return fmt.Errorf("DHCP listen %s: %w", s.listen, err)
	}

	slog.Info("DHCP server listening", "addr", s.listen, "server_ip", s.serverIP)

	for _, cfg := range s.subnets.List() {
		if store, ok := s.subnets.GetLeaseStore(cfg.Name); ok {
			s.reaper.Start(ctx, cfg, store)
		}
	}

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 65536)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				slog.Error("DHCP read error", "err", err)
				continue
			}
		}
		s.handlePacket(buf[:n], addr, conn)
	}
}

// StartReaper starts a reaper goroutine for a newly added subnet.
func (s *Server) StartReaper(ctx context.Context, cfg subnet.Config, store lease.Store) {
	s.reaper.Start(ctx, cfg, store)
}

func (s *Server) handlePacket(data []byte, _ net.Addr, conn net.PacketConn) {
	pkt, err := Parse(data)
	if err != nil {
		slog.Debug("DHCP parse error", "err", err)
		return
	}

	if pkt.Op != OpRequest {
		return
	}

	subnetCfg, store, ok := s.subnets.ForPacket(pkt.GIAddr, s.serverIP)
	if !ok {
		slog.Debug("no subnet matched", "giaddr", pkt.GIAddr, "server_ip", s.serverIP)
		return
	}

	var device *ztp.DeviceRecord
	var profile *ztp.VendorProfile
	if s.ztpMgr != nil {
		device, profile = s.ztpMgr.LookupForPacket(pkt.CHAddr, pkt.Options.VendorClass())
	}

	reply := s.handler.Dispatch(pkt, subnetCfg, store, device, profile)
	if reply == nil {
		return
	}

	if err := s.sendReply(reply, pkt, conn); err != nil {
		slog.Error("DHCP send error", "err", err)
	}
}

func (s *Server) sendReply(reply, req *Packet, conn net.PacketConn) error {
	data := reply.Serialize()

	var dst net.Addr

	if req.GIAddr != nil && !req.GIAddr.IsUnspecified() {
		dst = &net.UDPAddr{IP: req.GIAddr, Port: 67}
	} else if req.BroadcastRequested() {
		dst = &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
	} else {
		ip := reply.YIAddr
		if ip == nil || ip.IsUnspecified() {
			dst = &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
		} else {
			dst = &net.UDPAddr{IP: ip, Port: 68}
		}
	}

	_, err := conn.WriteTo(data, dst)
	return err
}

func listenDHCP(addr string) (net.PacketConn, error) {
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var setsockoptErr error
			err := c.Control(func(fd uintptr) {
				setsockoptErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
			})
			if err != nil {
				return err
			}
			return setsockoptErr
		},
	}
	return lc.ListenPacket(context.Background(), "udp4", addr)
}

// Reaper sweeps expired and declined leases on a per-subnet schedule.
type Reaper struct {
	plugins *plugin.Registry
}

func NewReaper(plugins *plugin.Registry) *Reaper {
	return &Reaper{plugins: plugins}
}

func (r *Reaper) Start(ctx context.Context, cfg subnet.Config, store lease.Store) {
	interval := cfg.LeaseReaperInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	go r.run(ctx, cfg, store, interval)
}

func (r *Reaper) run(ctx context.Context, cfg subnet.Config, store lease.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(cfg, store)
		}
	}
}

func (r *Reaper) sweep(cfg subnet.Config, store lease.Store) {
	for _, l := range store.Expired() {
		slog.Debug("lease expired", "subnet", cfg.Name, "ip", l.IP, "client", l.ClientID)
		r.plugins.EmitLeaseEvent(plugin.LeaseEvent{
			Type:       plugin.EventExpired,
			IP:         l.IP,
			ClientID:   l.ClientID,
			ClientHW:   l.ClientHW,
			GiAddr:     l.GiAddr,
			SubnetName: cfg.Name,
			SubnetCIDR: cfg.Network.String(),
		})
	}
	for range store.Declined() {
		// Declined IPs return to pool silently per design
	}
}
