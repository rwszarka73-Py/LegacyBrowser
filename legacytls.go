package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Old devices (iLO 2/3) speak TLS 1.0/1.1 with RSA key exchange and CBC ciphers.
// Some firmware also drops the connection when offered TLS 1.2 instead of
// negotiating down, so like old browsers we retry with lower maximum versions.

var legacySuites = []uint16{
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
	tls.TLS_RSA_WITH_RC4_128_SHA,
}

var maxVersions = []uint16{tls.VersionTLS12, tls.VersionTLS11, tls.VersionTLS10}

var workingMax sync.Map // host:port -> index into maxVersions

func legacyConfig(host string, max uint16) *tls.Config {
	cfg := &tls.Config{
		InsecureSkipVerify: true, // devices use self-signed certificates
		MinVersion:         tls.VersionTLS10,
		MaxVersion:         max,
		CipherSuites:       legacySuites,
	}
	if net.ParseIP(host) == nil {
		cfg.ServerName = host
	}
	return cfg
}

// dialLegacy opens a TLS connection to an old device, retrying with older versions.
func dialLegacy(addr string) (*tls.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		addr = net.JoinHostPort(addr, "443")
	}
	start := 0
	if v, ok := workingMax.Load(addr); ok {
		start = v.(int)
	}
	var lastErr error
	for i := start; i < len(maxVersions); i++ {
		raw, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("can't reach %s: %w", addr, err)
		}
		c := tls.Client(raw, legacyConfig(host, maxVersions[i]))
		_ = c.SetDeadline(time.Now().Add(15 * time.Second))
		if err := c.Handshake(); err != nil {
			lastErr = err
			logf("%s: TLS (max %s) failed: %v", addr, tls.VersionName(maxVersions[i]), err)
			raw.Close()
			continue
		}
		_ = c.SetDeadline(time.Time{})
		if prev, ok := workingMax.Load(addr); !ok || prev.(int) != i {
			st := c.ConnectionState()
			logf("%s: connected using %s / %s", addr, tls.VersionName(st.Version), tls.CipherSuiteName(st.CipherSuite))
			workingMax.Store(addr, i)
		}
		return c, nil
	}
	return nil, fmt.Errorf("TLS handshake with %s failed: %v", addr, lastErr)
}

// hostPort normalizes "host", "host:port", "https://host/..." into host:port.
func hostPort(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		s = net.JoinHostPort(strings.Trim(s, "[]"), "443")
	}
	return s
}
