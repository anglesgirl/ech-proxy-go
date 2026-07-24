// Package config 处理 ECH 代理的配置
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 代理配置
type Config struct {
	Listen   string   `yaml:"listen"`
	DoH      string   `yaml:"doh"`
	Mode     string   `yaml:"mode"` // "http", "socks5", "both"
	LogLevel string   `yaml:"log_level"`
	TLS      TLSConfig `yaml:"tls"`
	DNS      DNSConfig `yaml:"dns"`
	Proxy    ProxyConfig `yaml:"proxy"`
}

type TLSConfig struct {
	Timeout       string `yaml:"timeout"`
	SkipVerify    bool   `yaml:"skip_verify"`
	FallbackPlain bool   `yaml:"fallback_plain"` // ECH 失败后回退普通 TLS
}

type DNSConfig struct {
	CacheTTL     string `yaml:"cache_ttl"`
	Timeout      string `yaml:"timeout"`
	PreferIPv4   bool   `yaml:"prefer_ipv4"`
}

type ProxyConfig struct {
	ConnectTimeout string `yaml:"connect_timeout"`
	IdleTimeout    string `yaml:"idle_timeout"`
}

// Default 返回默认配置
func Default() *Config {
	return &Config{
		Listen:   "127.0.0.1:17171",
		DoH:      "https://1.1.1.1/dns-query",
		Mode:     "http",
		LogLevel: "info",
		TLS: TLSConfig{
			Timeout:       "15s",
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
	}
}

// Load 从文件加载配置，不存在则返回默认值
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

	// 命令行参数覆盖
	if v := os.Getenv("DOH_URL"); v != "" {
		cfg.DoH = v
	}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		cfg.Listen = v
	}

	return cfg, nil
}

// Validate 检查配置合法性
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

	if !strings.HasPrefix(c.DoH, "https://") {
		return fmt.Errorf("doh URL must use HTTPS")
	}

	return nil
}
