package mailimage

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type lookupFunc func(context.Context, string, string) ([]netip.Addr, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

// PublicURL performs syntactic checks only. No DNS query is made until Fetch.
func PublicURL(source string) bool {
	if len(source) > 16<<10 {
		return false
	}
	u, err := url.Parse(source)
	if err != nil || (strings.ToLower(u.Scheme) != "https" && strings.ToLower(u.Scheme) != "http") || u.User != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicAddress(ip)
	}
	return strings.Contains(host, ".") && host != "localhost" && !strings.HasSuffix(host, ".localhost") && !strings.HasSuffix(host, ".local") && !strings.HasSuffix(host, ".internal") && !strings.HasSuffix(host, ".localdomain")
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range reserved {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func client(parent context.Context, lookup lookupFunc, dial dialFunc) (*http.Client, *http.Transport) {
	transport := &http.Transport{
		// Do not inherit proxies, cookies, auth, or credentials from mail or the
		// environment. Resolve and validate every answer, then pin the dial.
		Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10,
		TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		DialContext: func(transportContext context.Context, network, address string) (net.Conn, error) {
			// Transport may detach its dial context to reuse connections. Bind
			// DNS and dialing to this explicit image request as well, so hiding
			// images cancels resolution before it can start another connection.
			ctx, cancel := context.WithTimeout(parent, 8*time.Second)
			stop := context.AfterFunc(transportContext, cancel)
			defer stop()
			defer cancel()
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, errors.New("invalid image host")
			}
			addresses, err := lookup(ctx, "ip", host)
			if err != nil || len(addresses) == 0 {
				return nil, errors.New("could not resolve image host")
			}
			for _, ip := range addresses {
				if !publicAddress(ip) {
					return nil, errors.New("image host resolves to a non-public address")
				}
			}
			for _, ip := range addresses {
				conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
			}
			return nil, errors.New("could not connect to image host")
		},
	}
	return &http.Client{
		Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: imageRedirect,
	}, transport
}

func imageRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 3 || !PublicURL(req.URL.String()) {
		return errors.New("image redirect is not a public HTTP(S) source")
	}
	req.URL.Scheme = strings.ToLower(req.URL.Scheme)
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("image redirect would downgrade HTTPS")
	}
	// A redirect must not disclose the original image URL (which may contain
	// a signed query or per-recipient token) in a Referer header.
	req.Header.Del("Referer")
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	return nil
}

// Fetch contacts a public image host after the reader permits remote images. DNS,
// connection, redirects, and body reads share a deadline. Every redirect is
// revalidated and every connection is pinned to approved public DNS answers.
func Fetch(ctx context.Context, source string, limit int64) ([]byte, string, error) {
	if !PublicURL(source) {
		return nil, "", errors.New("only public HTTP(S) image sources can be loaded")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u, _ := url.Parse(source)
	u.Scheme = strings.ToLower(u.Scheme)
	resolver := &net.Resolver{PreferGo: true}
	httpClient, transport := client(ctx, resolver.LookupNetIP, (&net.Dialer{Timeout: 8 * time.Second}).DialContext)
	defer transport.CloseIdleConnections()
	return fetch(ctx, httpClient, u.String(), min(limit, MaxBytes))
}

func fetch(ctx context.Context, httpClient *http.Client, source string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, "", errors.New("invalid image request")
	}
	req.Header.Set("User-Agent", "Hane image viewer")
	req.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/webp")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", errors.New("image request failed or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, "", errors.New("image redirects are blocked")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("image host returned HTTP %d", resp.StatusCode)
	}
	if limit <= 0 || resp.ContentLength > limit {
		return nil, "", errors.New("image exceeds the download limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, "", errors.New("image download was incomplete or too large")
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func escapeColor(top, bottom color.RGBA) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm", top.R, top.G, top.B, bottom.R, bottom.G, bottom.B)
}
