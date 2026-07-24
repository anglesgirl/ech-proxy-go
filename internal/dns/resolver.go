// Package dns 实现 DoH 查询和 DNS 缓存
package dns

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ECHConfig 存储 ECH 配置
type ECHConfig struct {
	Config []byte // 原始 ECHConfigList 字节
}

// Result DNS 查询结果
type Result struct {
	IPs      []net.IP
	ECH      *ECHConfig
	OuterSNI string // HTTPS 记录中的 outer SNI
	ExpireAt time.Time
}

// Resolver DoH 解析器，带 TTL 缓存
type Resolver struct {
	dohURL   string
	timeout  time.Duration
	cacheTTL time.Duration
	client   *http.Client

	mu    sync.RWMutex
	cache map[string]*Result
}

// New 创建 DoH 解析器
func New(dohURL string, timeout, cacheTTL time.Duration) *Resolver {
	return &Resolver{
		dohURL:   dohURL,
		timeout:  timeout,
		cacheTTL: cacheTTL,
		client:   &http.Client{Timeout: timeout},
		cache:    make(map[string]*Result),
	}
}

// Lookup 查询域名，优先返回缓存结果
func (r *Resolver) Lookup(hostname string, preferIPv4 bool) (*Result, error) {
	// 查缓存
	r.mu.RLock()
	if cached, ok := r.cache[hostname]; ok && time.Now().Before(cached.ExpireAt) {
		r.mu.RUnlock()
		return cached, nil
	}
	r.mu.RUnlock()

	// 并发查询 A/AAAA/HTTPS
	result, err := r.dohLookup(hostname)
	if err != nil {
		return nil, err
	}

	// IP 排序：优先 IPv4
	if preferIPv4 {
		r.sortIPv4First(result.IPs)
	}

	// 写缓存
	result.ExpireAt = time.Now().Add(r.cacheTTL)
	r.mu.Lock()
	r.cache[hostname] = result
	r.mu.Unlock()

	return result, nil
}

// ClearCache 清空 DNS 缓存
func (r *Resolver) ClearCache() {
	r.mu.Lock()
	r.cache = make(map[string]*Result)
	r.mu.Unlock()
}

func (r *Resolver) sortIPv4First(ips []net.IP) {
	for i := 0; i < len(ips); i++ {
		if ips[i].To4() != nil {
			// 把 IPv4 移到前面
			if i != 0 {
				ips[0], ips[i] = ips[i], ips[0]
			}
			break
		}
	}
}

func (r *Resolver) dohLookup(hostname string) (*Result, error) {
	result := &Result{}

	type queryResult struct {
		ips       []net.IP
		ech       *ECHConfig
		outerName string
		err       error
	}

	ch := make(chan queryResult, 3)

	go func() {
		ips, err := r.queryType(hostname, 1)
		ch <- queryResult{ips: ips, err: err}
	}()
	go func() {
		ips, err := r.queryType(hostname, 28)
		ch <- queryResult{ips: ips, err: err}
	}()
	go func() {
		ech, outerName, err := r.queryHTTPS(hostname)
		ch <- queryResult{ech: ech, outerName: outerName, err: err}
	}()

	for i := 0; i < 3; i++ {
		qr := <-ch
		if qr.err != nil {
			log.Printf("[dns] query error for %s: %v", hostname, qr.err)
			continue
		}
		if qr.ips != nil {
			result.IPs = append(result.IPs, qr.ips...)
		}
		if qr.ech != nil {
			result.ECH = qr.ech
		}
		if qr.outerName != "" {
			result.OuterSNI = qr.outerName
		}
	}

	if len(result.IPs) == 0 {
		return nil, fmt.Errorf("no DNS results for %s", hostname)
	}

	return result, nil
}

func (r *Resolver) queryType(hostname string, qtype int) ([]net.IP, error) {
	u, err := url.Parse(r.dohURL)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("name", hostname)
	q.Set("type", fmt.Sprintf("%d", qtype))
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var dohResp struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}

	if err := json.Unmarshal(body, &dohResp); err != nil {
		return nil, fmt.Errorf("JSON parse: %w (body: %s)", err, truncStr(string(body), 200))
	}
	if dohResp.Status != 0 {
		return nil, fmt.Errorf("DoH status: %d", dohResp.Status)
	}

	var ips []net.IP
	for _, ans := range dohResp.Answer {
		if ans.Type == qtype {
			switch qtype {
			case 1: // A
				if ip := net.ParseIP(ans.Data); ip != nil && ip.To4() != nil {
					ips = append(ips, ip)
				}
			case 28: // AAAA
				if ip := net.ParseIP(ans.Data); ip != nil && ip.To4() == nil {
					ips = append(ips, ip)
				}
			}
		}
	}
	return ips, nil
}

func (r *Resolver) queryHTTPS(hostname string) (*ECHConfig, string, error) {
	u, err := url.Parse(r.dohURL)
	if err != nil {
		return nil, "", err
	}

	q := u.Query()
	q.Set("name", hostname)
	q.Set("type", "65")
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/dns-json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	var dohResp struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}

	if err := json.Unmarshal(body, &dohResp); err != nil {
		return nil, "", fmt.Errorf("JSON parse: %w", err)
	}
	if dohResp.Status != 0 {
		return nil, "", fmt.Errorf("DoH status: %d", dohResp.Status)
	}

	for _, ans := range dohResp.Answer {
		if ans.Type != 65 {
			continue
		}
		data := ans.Data

		if strings.HasPrefix(data, "\\#") {
			ech, outerName, err := parseHTTPSRecordHex(data)
			if err != nil {
				log.Printf("[dns] parse HTTPS hex: %v", err)
				continue
			}
			if ech != nil {
				return ech, outerName, nil
			}
		}

		if strings.Contains(data, "ech=") {
			ech, outerName, err := parseHTTPSRecordText(data)
			if err != nil {
				log.Printf("[dns] parse HTTPS text: %v", err)
				continue
			}
			if ech != nil {
				return ech, outerName, nil
			}
		}
	}
	return nil, "", nil
}

// === HTTPS 记录解析 ===

func parseHTTPSRecordHex(data string) (*ECHConfig, string, error) {
	parts := strings.SplitN(data, " ", 3)
	if len(parts) < 3 {
		return nil, "", fmt.Errorf("invalid hex format")
	}
	hexStr := strings.ReplaceAll(parts[2], " ", "")
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, "", fmt.Errorf("hex decode: %w", err)
	}
	return parseHTTPSRecordBinary(raw)
}

func parseHTTPSRecordText(data string) (*ECHConfig, string, error) {
	parts := strings.Fields(data)
	if len(parts) < 2 {
		return nil, "", fmt.Errorf("invalid text format")
	}

	outerName := parts[1]
	if outerName == "." {
		outerName = ""
	}

	for _, p := range parts[2:] {
		if strings.HasPrefix(p, "ech=") {
			echBase64 := strings.TrimPrefix(p, "ech=")
			echBase64 = strings.Trim(echBase64, "\"")
			echBytes, err := base64.StdEncoding.DecodeString(echBase64)
			if err != nil {
				return nil, "", fmt.Errorf("ech base64 decode: %w", err)
			}
			return &ECHConfig{Config: echBytes}, outerName, nil
		}
	}
	return nil, outerName, nil
}

func parseHTTPSRecordBinary(raw []byte) (*ECHConfig, string, error) {
	if len(raw) < 2 {
		return nil, "", fmt.Errorf("record too short")
	}

	idx := 2 // 跳过 SvcPriority

	// TargetName (DNS 域名格式，以 0 结尾)
	outerName := ""
	for idx < len(raw) {
		labelLen := int(raw[idx])
		idx++
		if labelLen == 0 {
			break
		}
		if idx+labelLen > len(raw) {
			return nil, "", fmt.Errorf("invalid target name")
		}
		label := string(raw[idx : idx+labelLen])
		if outerName == "" {
			outerName = label
		} else {
			outerName += "." + label
		}
		idx += labelLen
	}

	// SvcParams
	for idx+4 <= len(raw) {
		svcKey := binary.BigEndian.Uint16(raw[idx:])
		valLen := int(binary.BigEndian.Uint16(raw[idx+2:]))
		idx += 4
		if idx+valLen > len(raw) {
			break
		}
		val := raw[idx : idx+valLen]
		idx += valLen

		// svcKey 5 = ech
		if svcKey == 5 && len(val) >= 2 {
			return &ECHConfig{Config: val}, outerName, nil
		}
	}
	return nil, outerName, nil
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
