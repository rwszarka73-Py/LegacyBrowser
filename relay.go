package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Web interface relay: http://127.0.0.1:<port>/  ->  https://<device>/ (old TLS)
// ---------------------------------------------------------------------------

type webRelay struct {
	target string // host:port
	local  string // http://127.0.0.1:N
	srv    *http.Server
}

func legacyTransport() *http.Transport {
	return &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialLegacy(addr)
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
}

func startWebRelay(target string) (*webRelay, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &webRelay{target: target, local: "http://" + ln.Addr().String()}
	host, port, _ := net.SplitHostPort(target)
	// The device refers to itself as https://host or https://host:443
	selfNames := []string{"https://" + target, "http://" + target}
	if port == "443" {
		selfNames = append(selfNames, "https://"+host, "http://"+host)
	}

	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "https", Host: target})
	proxy.Transport = legacyTransport()
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.Host = hostHeader(target)
		if req.Header.Get("Origin") != "" {
			req.Header.Set("Origin", "https://"+hostHeader(target))
		}
		if ref := req.Header.Get("Referer"); ref != "" {
			req.Header.Set("Referer", strings.Replace(ref, r.local, "https://"+hostHeader(target), 1))
		}
		req.Header.Del("Accept-Encoding") // keep bodies plain so links can be rewritten
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if loc := resp.Header.Get("Location"); loc != "" {
			for _, n := range selfNames {
				if strings.HasPrefix(loc, n) {
					loc = r.local + strings.TrimPrefix(loc, n)
					break
				}
			}
			resp.Header.Set("Location", loc)
		}
		if cs := resp.Header.Values("Set-Cookie"); len(cs) > 0 {
			resp.Header.Del("Set-Cookie")
			for _, c := range cs {
				resp.Header.Add("Set-Cookie", relaxCookie(c))
			}
		}
		resp.Header.Del("Strict-Transport-Security")
		ct := resp.Header.Get("Content-Type")
		if strings.Contains(ct, "text/") || strings.Contains(ct, "javascript") || strings.Contains(ct, "json") {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return err
			}
			s := string(body)
			for _, n := range selfNames {
				s = strings.ReplaceAll(s, n, r.local)
			}
			resp.Body = io.NopCloser(strings.NewReader(s))
			resp.ContentLength = int64(len(s))
			resp.Header.Set("Content-Length", strconv.Itoa(len(s)))
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		logf("web %s%s: %v", target, req.URL.Path, err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "Legacy Browser couldn't reach %s:\n\n%v\n", target, err)
	}
	r.srv = &http.Server{Handler: proxy, ReadHeaderTimeout: 30 * time.Second}
	go r.srv.Serve(ln)
	logf("web interface for %s at %s", target, r.local)
	return r, nil
}

func hostHeader(target string) string {
	h, p, _ := net.SplitHostPort(target)
	if p == "443" {
		return h
	}
	return target
}

// relaxCookie drops Secure/Domain/SameSite=None so cookies work on http://127.0.0.1.
func relaxCookie(c string) string {
	parts := strings.Split(c, ";")
	out := parts[:1]
	for _, p := range parts[1:] {
		k := strings.ToLower(strings.TrimSpace(p))
		if k == "secure" || strings.HasPrefix(k, "domain=") || strings.HasPrefix(k, "samesite=none") {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}

// ---------------------------------------------------------------------------
// HTTPS proxy for the Java console: CONNECT host:port -> local TLS (self-signed)
// on one side, old TLS to the device on the other.
// ---------------------------------------------------------------------------

type connectRelay struct {
	ln    net.Listener
	certs sync.Map // host -> *tls.Certificate
}

func startConnectRelay() (*connectRelay, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &connectRelay{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go r.handle(c)
		}
	}()
	return r, nil
}

func (r *connectRelay) port() int { return r.ln.Addr().(*net.TCPAddr).Port }

func (r *connectRelay) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	target := req.Host
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
		target = net.JoinHostPort(target, "443")
	}
	dev, err := dialLegacy(target)
	if err != nil {
		logf("console relay: %v", err)
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer dev.Close()
	io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")

	cert, err := r.certFor(host)
	if err != nil {
		return
	}
	local := tls.Server(&prefixConn{Conn: c, r: br}, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS10,
	})
	if err := local.Handshake(); err != nil {
		logf("console relay: local TLS with Java failed: %v", err)
		return
	}
	defer local.Close()
	pipe(local, dev)
}

func (r *connectRelay) certFor(host string) (*tls.Certificate, error) {
	if v, ok := r.certs.Load(host); ok {
		return v.(*tls.Certificate), nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host, Organization: []string{appName}},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(2 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	r.certs.Store(host, cert)
	return cert, nil
}

// prefixConn replays bytes already buffered by the request reader.
type prefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *prefixConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}

// ---------------------------------------------------------------------------
// Simple HTTPS client for logging in and downloading the console (old TLS).
// ---------------------------------------------------------------------------

func legacyClient() *http.Client {
	return &http.Client{
		Transport: legacyTransport(),
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // we handle redirects/cookies ourselves
		},
	}
}
