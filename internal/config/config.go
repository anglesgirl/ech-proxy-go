// Package config handles ECH proxy configuration.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds all proxy settings.
type Config struct {
	Listen   string     `yaml:"listen"`
	DoH      string     `yaml:"doh"`
	Mode     string     `yaml:"mode"` // "http", "socks5", "both"
	LogLevel string     `yaml:"log_level"`
	TLS      TLSConfig  `yaml:"tls"`
	DNS      DNSConfig  `yaml:"dns"`
	Proxy    ProxyConfig `yaml:"proxy"`
	ECH      ECHConfig  `yaml:"ech"`
	MITM     MITMConfig `yaml:"mitm"`
}

// MITMConfig 控制 CONNECT MITM 模式：代理下游用自签证书终止客户端 TLS，
// 上游用 ECH/普通 TLS 连真实服务器。客户端需信任代理 CA 或跳过证书校验。
type MITMConfig struct {
	Enabled bool `yaml:"enabled"`
}

type TLSConfig struct {
	Timeout       string `yaml:"timeout"`
	SkipVerify    bool   `yaml:"skip_verify"`
	FallbackPlain bool   `yaml:"fallback_plain"`
}

type DNSConfig struct {
	CacheTTL   string `yaml:"cache_ttl"`
	Timeout    string `yaml:"timeout"`
	PreferIPv4 bool   `yaml:"prefer_ipv4"`
	CachePath  string `yaml:"cache_path"` // file-based ECH config cache
}

type ProxyConfig struct {
	ConnectTimeout string `yaml:"connect_timeout"`
	IdleTimeout    string `yaml:"idle_timeout"`
}

// ECHConfig controls ECH-specific behavior.
type ECHConfig struct {
	// CustomIPs: comma-separated Cloudflare edge IPs to try before DNS.
	// Only AS13335 IPs are accepted; others are silently filtered out.
	CustomIPs string `yaml:"custom_ips"`
	// NoDowngrade: when true, hosts with ECH config never fall back to plain TLS.
	// This protects SNI from leaking — set true for censorship-resistant deployments.
	NoDowngrade bool `yaml:"no_downgrade"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		Listen:   "127.0.0.1:17171",
		DoH:      "https://1.1.1.1/dns-query",
		Mode:     "http",
		LogLevel: "info",
		TLS: TLSConfig{
			Timeout:       "5s",
			SkipVerify:    false,
			FallbackPlain: true,
		},
		DNS: DNSConfig{
			CacheTTL:   "300s",
			Timeout:    "10s",
			PreferIPv4: true,
		},
		Proxy: ProxyConfig{
			ConnectTimeout: "10s",
			IdleTimeout:    "120s",
		},
		ECH: ECHConfig{
			NoDowngrade: false,
		},
	}
}

// Load reads config from a file; returns defaults if the file doesn't exist.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Environment variable overrides.
	if v := os.Getenv("DOH_URL"); v != "" {
		cfg.DoH = v
	}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("ECH_CUSTOM_IPS"); v != "" {
		cfg.ECH.CustomIPs = v
	}

	return cfg, nil
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	c.Mode = strings.ToLower(c.Mode)
	switch c.Mode {
	case "http", "socks5", "both":
	default:
		return fmt.Errorf("invalid mode: %s (must be http/socks5/both)", c.Mode)
	}

	if c.DoH == "" {
		return fmt.Errorf("doh URL is required")
	}

	for _, u := range strings.Split(c.DoH, ",") {
		u = strings.TrimSpace(u)
		if u != "" && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("doh URL must use HTTPS: %s", u)
		}
	}

	// No-downgrade overrides fallback_plain: if the user set no_downgrade,
	// plain TLS fallback is disabled regardless of the tls.fallback_plain flag.
	if c.ECH.NoDowngrade {
		c.TLS.FallbackPlain = false
	}

	return nil
}
