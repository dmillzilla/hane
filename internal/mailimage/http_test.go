package mailimage

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestImageURLRejectsLocalCredentialsAndUnsupportedSchemes(t *testing.T) {
	for _, source := range []string{
		"file:///etc/passwd", "ftp://images.example/image.png", "javascript:alert(1)",
		"http://localhost/image", "http://device.local/image", "http://internal/image",
		"http://127.0.0.1/image", "http://10.0.0.1/image", "http://169.254.169.254/latest",
		"http://100.64.0.1/image", "http://192.168.1.1/image", "http://[::1]/image",
		"http://[::ffff:127.0.0.1]/image", "http://[fec0::1]/image", "http://[::127.0.0.1]/image",
		"http://user:password@images.example/image.png", "http://device.local./image",
	} {
		if PublicURL(source) {
			t.Fatalf("unsafe image URL allowed: %s", source)
		}
	}
	for _, source := range []string{"https://images.example/photo.png", "http://8.8.8.8/photo.png", "https://[2606:4700:4700::1111]/image"} {
		if !PublicURL(source) {
			t.Fatalf("public URL rejected: %s", source)
		}
	}
}

func TestPrivateDNSAnswersNeverDialEvenWithPublicAnswer(t *testing.T) {
	for _, ips := range [][]netip.Addr{
		{netip.MustParseAddr("127.0.0.1")},
		{netip.MustParseAddr("169.254.169.254")},
		{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("192.168.1.1")},
		{netip.MustParseAddr("::ffff:10.0.0.1")},
	} {
		var dials atomic.Int32
		lookup := func(context.Context, string, string) ([]netip.Addr, error) { return ips, nil }
		client, transport := client(context.Background(), lookup, func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, nil
		})
		_, _, err := fetch(context.Background(), client, "http://images.example/photo.png", MaxBytes)
		transport.CloseIdleConnections()
		if err == nil || dials.Load() != 0 {
			t.Fatal("a private or mixed DNS answer reached the dialer")
		}
	}
}

func TestPublicFetchPinsAddressAndSendsNoMailCredentials(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	raw := pngFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.Method != http.MethodGet {
			t.Error("image request carried credentials or mail context")
		}
		if !strings.HasPrefix(r.Host, "images.example:") {
			t.Error("pinned dial did not preserve the image host")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	serverURL, _ := url.Parse(server.URL)
	var dials atomic.Int32
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	client, transport := client(context.Background(), lookup, func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if address != net.JoinHostPort("93.184.216.34", serverURL.Port()) {
			t.Error("connection was not pinned to the approved address")
		}
		return (&net.Dialer{}).DialContext(ctx, network, serverURL.Host)
	})
	defer transport.CloseIdleConnections()
	data, kind, err := fetch(context.Background(), client, "http://images.example:"+serverURL.Port()+"/photo.png", MaxBytes)
	if err != nil || kind != "image/png" || len(data) != len(raw) || requests.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("pinned request failed: %v", err)
	}
}

func TestRedirectAndOversizedDownloadsAreRefused(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = imageRedirect
	if _, _, err := fetch(context.Background(), client, server.URL+"/redirect", MaxBytes); err == nil || requests.Load() != 1 {
		t.Fatal("redirect was followed or accepted")
	}
	if _, _, err := fetch(context.Background(), client, server.URL+"/large", 10); err == nil {
		t.Fatal("oversized image accepted")
	}
}

func TestCanceledImageRequestStopsDNS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stopped := make(chan struct{})
	lookup := func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	client, transport := client(ctx, lookup, (&net.Dialer{}).DialContext)
	defer transport.CloseIdleConnections()
	started := time.Now()
	if _, _, err := fetch(ctx, client, "http://images.example/photo", MaxBytes); err == nil || time.Since(started) > time.Second {
		t.Fatal("canceled request did not stop promptly")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("canceled HTTP request left DNS resolution running")
	}
}

func TestPublicRedirectsAreRevalidatedAndDoNotSendReferer(t *testing.T) {
	var requests, resolutions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/start" {
			_, port, _ := net.SplitHostPort(r.Host)
			http.Redirect(w, r, "http://cdn.example:"+port+"/final", http.StatusFound)
			return
		}
		if r.Header.Get("Referer") != "" {
			t.Error("redirect disclosed the original image URL")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngFixture(t))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		resolutions.Add(1)
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	client, transport := client(context.Background(), lookup, func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	})
	defer transport.CloseIdleConnections()
	if _, _, err := fetch(context.Background(), client, "http://images.example:"+u.Port()+"/start?recipient=private-token", MaxBytes); err != nil || requests.Load() != 2 || resolutions.Load() != 2 {
		t.Fatalf("public redirect was not fetched through revalidated DNS: %v", err)
	}
}

func TestRedirectDNSRebindingCannotReachPrivateAddress(t *testing.T) {
	var dials, lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, port, _ := net.SplitHostPort(r.Host)
		http.Redirect(w, r, "http://cdn.example:"+port+"/image", http.StatusFound)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	}
	client, transport := client(context.Background(), lookup, func(ctx context.Context, network, _ string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	})
	defer transport.CloseIdleConnections()
	if _, _, err := fetch(context.Background(), client, "http://images.example:"+u.Port()+"/start", MaxBytes); err == nil || dials.Load() != 1 {
		t.Fatal("redirect reached a private address after DNS changed")
	}
}
