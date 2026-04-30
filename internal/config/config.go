package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type ServerConfig struct {
	Listen   string `json:"listen"`
	ServerIP string `json:"server_ip"`
}

type APIConfig struct {
	Listen string `json:"listen"`
}

type TFTPConfig struct {
	Listen string `json:"listen"`
}

type ZTPConfig struct {
	Enabled bool       `json:"enabled"`
	TFTP    TFTPConfig `json:"tftp"`
}

type SubnetConfig struct {
	Name                string        `json:"name"`
	Network             string        `json:"network"`
	Router              string        `json:"router"`
	DNS                 []string      `json:"dns"`
	LeaseTime           Duration      `json:"lease_time"`
	OfferTimeout        Duration      `json:"offer_timeout"`
	DeclineCooldown     Duration      `json:"decline_cooldown"`
	LeaseReaperInterval Duration      `json:"lease_reaper_interval"`
	RangeStart          string        `json:"range_start"`
	RangeEnd            string        `json:"range_end"`
}

type LoggingConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type PluginConfig struct {
	Name    string            `json:"name"`
	Options map[string]string `json:"options"`
}

type RelayInterface struct {
	Name    string `json:"name"`
	AgentIP string `json:"agent_ip"`
}

type RelayConfig struct {
	Listen     string           `json:"listen"`
	Upstream   string           `json:"upstream"`
	Interfaces []RelayInterface `json:"interfaces"`
}

type Config struct {
	Server  ServerConfig   `json:"server"`
	API     APIConfig      `json:"api"`
	ZTP     ZTPConfig      `json:"ztp"`
	Subnets []SubnetConfig `json:"subnets"`
	Logging LoggingConfig  `json:"logging"`
	Plugins []PluginConfig `json:"plugins"`
	Relay   RelayConfig    `json:"relay"`
}

// Duration wraps time.Duration for JSON unmarshalling from strings like "24h".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

var defaults = Config{
	Server: ServerConfig{
		Listen:   "0.0.0.0:67",
		ServerIP: "127.0.0.1",
	},
	API: APIConfig{
		Listen: "127.0.0.1:8067",
	},
	ZTP: ZTPConfig{
		Enabled: true,
		TFTP: TFTPConfig{
			Listen: "0.0.0.0:69",
		},
	},
	Logging: LoggingConfig{
		Level:  "info",
		Format: "text",
	},
}

func Load(path string) (*Config, error) {
	cfg := defaults

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config %q: %w", path, err)
	}
	defer f.Close()

	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if cfg.Server.ServerIP == "" {
		return fmt.Errorf("server.server_ip is required")
	}
	for i, s := range cfg.Subnets {
		if s.Name == "" {
			return fmt.Errorf("subnets[%d]: name is required", i)
		}
		if s.Network == "" {
			return fmt.Errorf("subnet %q: network is required", s.Name)
		}
		if s.RangeStart == "" || s.RangeEnd == "" {
			return fmt.Errorf("subnet %q: range_start and range_end are required", s.Name)
		}
		if s.LeaseTime.Duration == 0 {
			cfg.Subnets[i].LeaseTime = Duration{24 * time.Hour}
		}
		if s.OfferTimeout.Duration == 0 {
			cfg.Subnets[i].OfferTimeout = Duration{30 * time.Second}
		}
		if s.DeclineCooldown.Duration == 0 {
			cfg.Subnets[i].DeclineCooldown = Duration{10 * time.Minute}
		}
		if s.LeaseReaperInterval.Duration == 0 {
			cfg.Subnets[i].LeaseReaperInterval = Duration{60 * time.Second}
		}
	}
	return nil
}
