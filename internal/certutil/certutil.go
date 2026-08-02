// Package certutil loads Android's system certificate store.
//
// CGO-free Go binaries do not reliably inherit Android's trust store.
// This package scans the standard Android certificate directories and
// accepts both hashed DER certificates and PEM bundles.
package certutil

import (
	"crypto/x509"
	"log"
	"os"
	"path/filepath"
	"sync"
)

var (
	pool  *x509.CertPool
	once  sync.Once
)

// LoadAndroidCertPool returns Android's system CA certificate pool.
// On non-Android platforms the directories will not exist and the
// function returns nil, causing the TLS dialer to use the system's
// default root CAs instead.
func LoadAndroidCertPool() *x509.CertPool {
	once.Do(func() {
		p := x509.NewCertPool()
		loaded := 0
		for _, dir := range []string{
			"/system/etc/security/cacerts",
			"/apex/com.android.conscrypt/cacerts",
			"/system/etc/security/cacerts_google",
			"/data/misc/user/0/cacerts-added",
		} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					continue
				}
				// Android stores certs as hashed DER; try parsing as a
				// single certificate first, then fall back to PEM bundle.
				if cert, err := x509.ParseCertificate(data); err == nil {
					p.AddCert(cert)
					loaded++
				} else if p.AppendCertsFromPEM(data) {
					loaded++
				}
			}
		}
		if loaded > 0 {
			pool = p
			log.Printf("[certutil] loaded %d Android system certificates", loaded)
		}
	})
	return pool
}
