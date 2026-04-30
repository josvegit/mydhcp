package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/josvegit/mydhcp/internal/api"
	"github.com/josvegit/mydhcp/internal/config"
	"github.com/josvegit/mydhcp/internal/dhcp"
	"github.com/josvegit/mydhcp/internal/plugin"
	"github.com/josvegit/mydhcp/internal/relay"
	"github.com/josvegit/mydhcp/internal/subnet"
	"github.com/josvegit/mydhcp/internal/ztp"
	"github.com/josvegit/mydhcp/plugins/auditlog"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		runServer(os.Args[1:])
		return
	}

	switch os.Args[1] {
	case "server":
		runServer(os.Args[2:])
	case "relay":
		runRelay(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\nusage: mydhcp [server|relay|version]\n", os.Args[1])
		os.Exit(1)
	}
}

func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/mydhcp/config.json", "path to config file")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	setupLogging(cfg.Logging.Level, cfg.Logging.Format)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Subnet manager
	subnetMgr := subnet.NewManager()
	for _, sc := range cfg.Subnets {
		subnetCfg, err := configToSubnet(sc)
		if err != nil {
			slog.Error("invalid subnet config", "name", sc.Name, "err", err)
			os.Exit(1)
		}
		if err := subnetMgr.Add(subnetCfg); err != nil {
			slog.Error("failed to add subnet", "name", sc.Name, "err", err)
			os.Exit(1)
		}
		slog.Info("subnet loaded", "name", sc.Name, "network", sc.Network)
	}

	// Plugin registry
	pluginReg := plugin.NewRegistry()
	for _, pc := range cfg.Plugins {
		switch pc.Name {
		case "auditlog":
			path := pc.Options["path"]
			if path == "" {
				path = "/var/log/mydhcp/audit.log"
			}
			al, err := auditlog.New(path)
			if err != nil {
				slog.Error("failed to init auditlog plugin", "err", err)
				os.Exit(1)
			}
			pluginReg.Register(al)
			slog.Info("plugin loaded", "name", "auditlog", "path", path)
		default:
			slog.Warn("unknown plugin, skipping", "name", pc.Name)
		}
	}

	// ZTP manager
	var ztpMgr *ztp.Manager
	if cfg.ZTP.Enabled {
		ztpMgr = ztp.NewManager(subnetMgr)
		slog.Info("ZTP enabled")
	}

	// DHCP server
	serverIP := net.ParseIP(cfg.Server.ServerIP)
	if serverIP == nil {
		slog.Error("invalid server_ip", "value", cfg.Server.ServerIP)
		os.Exit(1)
	}

	dhcpSrv := dhcp.NewServer(dhcp.ServerConfig{
		Listen:   cfg.Server.Listen,
		ServerIP: serverIP.To4(),
	}, subnetMgr, ztpMgr, pluginReg)

	// API server
	apiSrv := api.NewServer(cfg.API.Listen, subnetMgr, ztpMgr)

	// TFTP server
	var tftpErrCh chan error
	if cfg.ZTP.Enabled && ztpMgr != nil {
		tftpErrCh = make(chan error, 1)
		go func() {
			tftpErrCh <- ztp.ServeTFTP(
				cfg.ZTP.TFTP.Listen,
				ztpMgr,
				func(ip net.IP) (net.IPMask, net.IP, bool) {
					sc, _, ok := subnetMgr.SubnetForIP(ip)
					if !ok {
						return nil, nil, false
					}
					return sc.Network.Mask, sc.Router, true
				},
			)
		}()
	}

	// Run DHCP and API concurrently
	errCh := make(chan error, 2)
	go func() { errCh <- dhcpSrv.Run(ctx) }()
	go func() { errCh <- apiSrv.Run(ctx) }()

	select {
	case err := <-errCh:
		if err != nil {
			slog.Error("server error", "err", err)
		}
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	pluginReg.Shutdown(shutdownCtx)
}

func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/mydhcp/config.json", "path to config file")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	setupLogging(cfg.Logging.Level, cfg.Logging.Format)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ifaces := make([]relay.Interface, 0, len(cfg.Relay.Interfaces))
	for _, ri := range cfg.Relay.Interfaces {
		ip := net.ParseIP(ri.AgentIP)
		if ip == nil {
			slog.Error("invalid agent_ip", "interface", ri.Name, "value", ri.AgentIP)
			os.Exit(1)
		}
		ifaces = append(ifaces, relay.Interface{
			Name:    ri.Name,
			AgentIP: ip.To4(),
		})
	}

	if len(ifaces) == 0 {
		slog.Error("no relay interfaces configured")
		os.Exit(1)
	}

	r := relay.New(relay.Config{
		Listen:     cfg.Relay.Listen,
		Upstream:   cfg.Relay.Upstream,
		Interfaces: ifaces,
	})

	slog.Info("starting relay", "upstream", cfg.Relay.Upstream)
	if err := r.Run(ctx); err != nil {
		slog.Error("relay error", "err", err)
		os.Exit(1)
	}
}

func configToSubnet(sc config.SubnetConfig) (subnet.Config, error) {
	_, network, err := net.ParseCIDR(sc.Network)
	if err != nil {
		return subnet.Config{}, fmt.Errorf("invalid network %q: %w", sc.Network, err)
	}

	router := net.ParseIP(sc.Router)
	if router == nil {
		return subnet.Config{}, fmt.Errorf("invalid router %q", sc.Router)
	}

	rangeStart := net.ParseIP(sc.RangeStart)
	rangeEnd := net.ParseIP(sc.RangeEnd)
	if rangeStart == nil || rangeEnd == nil {
		return subnet.Config{}, fmt.Errorf("invalid range: %s - %s", sc.RangeStart, sc.RangeEnd)
	}

	var dnsIPs []net.IP
	for _, d := range sc.DNS {
		ip := net.ParseIP(d)
		if ip == nil {
			return subnet.Config{}, fmt.Errorf("invalid DNS IP %q", d)
		}
		dnsIPs = append(dnsIPs, ip)
	}

	return subnet.Config{
		Name:                sc.Name,
		Network:             network,
		Router:              router.To4(),
		DNS:                 dnsIPs,
		BroadcastAddr:       subnet.BroadcastAddr(network),
		LeaseTime:           sc.LeaseTime.Duration,
		OfferTimeout:        sc.OfferTimeout.Duration,
		DeclineCooldown:     sc.DeclineCooldown.Duration,
		LeaseReaperInterval: sc.LeaseReaperInterval.Duration,
		RangeStart:          rangeStart.To4(),
		RangeEnd:            rangeEnd.To4(),
	}, nil
}

func setupLogging(level, format string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}
