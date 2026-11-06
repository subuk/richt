package source

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func trust(t *testing.T, servers ...*httptest.Server) string {
	t.Helper()
	var data []byte
	for _, server := range servers {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})...)
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func client(t *testing.T, options Options) *Client {
	t.Helper()
	c, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestURLPolicy(t *testing.T) {
	for _, raw := range []string{
		"", "http://example.test/a", "file:///tmp/a", "/tmp/a", "//example.test/a", "https:foo",
		"https://user:secret@example.test/a", "https://example.test/a?q=secret", "https://example.test/a?",
		"https://example.test/a#secret", "https://example.test/a#", "https://example.test/a\n",
		"https://example.test/%0a", "https://example.test/%5cprivate", "https://example.test/%2fprivate",
		"https://example.test:0/a", "https://example.test:65536/a", "https://example.test:/a",
		"https://example.test:bad/a", "https://[not-ip]/a", "https://a..test/a", "https://a..test./a", "https://a../a", "https://-a.test/a",
	} {
		t.Run(raw, func(t *testing.T) {
			if err := ValidateURL(raw); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
	for _, raw := range []string{"https://EXAMPLE.test:443", "https://127.0.0.1:8443/a", "https://[::1]:8443/a", "https://intranet/a", "https://example.test/a%20b"} {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
	got, err := Resolve("https://EXAMPLE.test:443/services/app.yaml", "../templates/a.gotmpl")
	if err != nil || got != "https://example.test/templates/a.gotmpl" {
		t.Fatalf("resolve = %q, %v", got, err)
	}
	for _, ref := range []string{"//elsewhere.test/a", "http://example.test/a", "file:/tmp/a", "a?secret", "a#secret", "https://user:secret@example.test/a"} {
		if _, err := Resolve("https://example.test/a", ref); err == nil {
			t.Errorf("accepted reference %q", ref)
		}
	}
}

func TestVerifiedTLSAndSafeErrors(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/failure" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "private-response-secret")
			return
		}
		fmt.Fprint(w, "trusted")
	}))
	defer s.Close()
	if _, err := client(t, Options{}).Read(context.Background(), s.URL, ""); err == nil || strings.Contains(err.Error(), s.URL) {
		t.Fatalf("untrusted TLS: %v", err)
	}
	c := client(t, Options{CAFile: trust(t, s)})
	doc, err := c.Read(context.Background(), s.URL, "")
	if err != nil || string(doc.Data) != "trusted" || doc.URL != s.URL+"/" {
		t.Fatalf("trusted TLS = %+v, %v", doc, err)
	}
	_, err = c.Read(context.Background(), s.URL+"/failure", "")
	if err == nil || err.Error() != "source returned HTTP status 401" {
		t.Fatalf("status error: %v", err)
	}
	for _, raw := range []string{"https://user:private-password@example.test/a", "https://example.test/a?private-query"} {
		_, err := c.Read(context.Background(), raw, "")
		if err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	for _, options := range []Options{
		{CAFile: "/private-path-secret"},
		{Auth: []Auth{{Origin: "https://user:private-password@example.test", BearerToken: "private-token"}}},
		{Auth: []Auth{{Origin: "https://example.test", BearerToken: "private-token", Username: "private-name"}}},
		{Auth: []Auth{{Origin: "https://example.test", BearerToken: "private-token\n"}}},
		{Auth: []Auth{{Origin: "https://example.test", BearerToken: "one"}, {Origin: "https://EXAMPLE.test:443", BearerToken: "two"}}},
	} {
		_, err := New(options)
		if err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("unsafe configuration error: %v", err)
		}
	}
}

func TestAuthenticationScopesAndRedirects(t *testing.T) {
	type observed struct{ authorization, cookie, referer string }
	var mu sync.Mutex
	requests := map[string]observed{}
	var destination string
	handler := func(prefix string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests[prefix+r.URL.Path] = observed{r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Header.Get("Referer")}
			mu.Unlock()
			w.Header().Set("Set-Cookie", "secret=private-cookie")
			switch r.URL.Path {
			case "/scoped/redirect":
				http.Redirect(w, r, destination+"/final", http.StatusFound)
			case "/scoped/unscoped":
				http.Redirect(w, r, "/public", http.StatusFound)
			default:
				fmt.Fprint(w, "ok")
			}
		}
	}
	a := httptest.NewTLSServer(handler("a"))
	defer a.Close()
	b := httptest.NewTLSServer(handler("b"))
	defer b.Close()
	destination = b.URL
	c := client(t, Options{CAFile: trust(t, a, b), Auth: []Auth{
		{Origin: a.URL, PathPrefix: "/scoped", BearerToken: "private-bearer"},
		{Origin: a.URL, PathPrefix: "/scoped/deep", Username: "private-user", Password: "private-pass"},
	}})
	for _, p := range []string{"/scoped", "/scoped/child", "/scopedness", "/scoped/deep/child", "/scoped/redirect", "/scoped/unscoped", "/scoped/../public", "/scoped/%2e%2e/public", "/scoped/%252e%252e/public", "/public/../scoped"} {
		if _, err := c.Read(context.Background(), a.URL+p, ""); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{"a/scoped", "a/scoped/child", "a/scoped/redirect", "a/scoped/unscoped"} {
		if requests[p].authorization != "Bearer private-bearer" {
			t.Errorf("%s auth = %q", p, requests[p].authorization)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, a.URL, nil)
	req.SetBasicAuth("private-user", "private-pass")
	if requests["a/scoped/deep/child"].authorization != req.Header.Get("Authorization") {
		t.Error("longest scope did not receive basic credentials")
	}
	for _, p := range []string{"a/scopedness", "a/public", "b/final"} {
		if got := requests[p]; got != (observed{}) {
			t.Errorf("%s leaked request state: %+v", p, got)
		}
	}
	for _, p := range []string{"a/scoped/../public", "a/scoped/%2e%2e/public", "a/public/../scoped"} {
		// URL.Path contains one decoded representation; encoded and literal
		// dot segments share the first observation key.
		if got := requests[p]; got != (observed{}) {
			t.Errorf("%s leaked scoped credentials: %+v", p, got)
		}
	}
}

func TestRedirectReauthorizesDestinationAndRejectsUnsafeLocations(t *testing.T) {
	var auth string
	dst := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		fmt.Fprint(w, "final")
	}))
	defer dst.Close()
	src := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		location := dst.URL + "/final"
		switch r.URL.Path {
		case "/http":
			location = "http://example.test/private-location"
		case "/query":
			location += "?private-signed-query"
		case "/loop":
			location = "/loop"
		}
		http.Redirect(w, r, location, http.StatusFound)
	}))
	defer src.Close()
	c := client(t, Options{CAFile: trust(t, src, dst), Auth: []Auth{{Origin: dst.URL, PathPrefix: "/final", BearerToken: "destination-token"}}, MaxRedirects: 2})
	doc, err := c.Read(context.Background(), src.URL, "")
	if err != nil || auth != "Bearer destination-token" || doc.URL != dst.URL+"/final" {
		t.Fatalf("redirect = %+v, %v, auth %q", doc, err, auth)
	}
	for _, p := range []string{"/http", "/query", "/loop"} {
		_, err := c.Read(context.Background(), src.URL+p, "")
		if err == nil || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), src.URL) {
			t.Errorf("redirect %s error = %v", p, err)
		}
	}
}

func TestLimitsIncludeDecodedBytes(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gzip" {
			w.Header().Set("Content-Encoding", "gzip")
			z := gzip.NewWriter(w)
			fmt.Fprint(z, strings.Repeat("z", 1024))
			z.Close()
			return
		}
		fmt.Fprint(w, "12345")
	}))
	defer s.Close()
	ca := trust(t, s)
	for _, tc := range []struct {
		name string
		opts Options
		urls []string
		want string
	}{
		{"document", Options{MaxBytes: 4}, []string{"/a"}, "document byte limit"},
		{"gzip", Options{MaxBytes: 100}, []string{"/gzip"}, "document byte limit"},
		{"total", Options{MaxTotalBytes: 9}, []string{"/a", "/b"}, "total byte limit"},
		{"count", Options{MaxDocuments: 1}, []string{"/a", "/b"}, "document limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.CAFile = ca
			c := client(t, tc.opts)
			var err error
			for _, p := range tc.urls {
				_, err = c.Read(context.Background(), s.URL+p, "")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSnapshotChecksumAndConcurrentCache(t *testing.T) {
	var hits atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "snapshot")
	}))
	defer s.Close()
	c := client(t, Options{CAFile: trust(t, s), MaxDocuments: 1, MaxTotalBytes: 8})
	sum := sha256.Sum256([]byte("snapshot"))
	digest := hex.EncodeToString(sum[:])
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			doc, err := c.Read(context.Background(), s.URL, strings.ToUpper(digest))
			if err != nil || string(doc.Data) != "snapshot" {
				t.Errorf("cached read = %+v, %v", doc, err)
				return
			}
			doc.Data[0] = 'X'
		})
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("requests = %d", hits.Load())
	}
	if _, err := c.Read(context.Background(), s.URL+"/", strings.Repeat("0", 64)); err == nil || err.Error() != "source SHA-256 mismatch" {
		t.Fatalf("checksum error = %v", err)
	}
	if hits.Load() != 1 {
		t.Fatal("digest mismatch refetched snapshot")
	}
	for _, invalid := range []string{"private-bad-digest", strings.Repeat("g", 64), strings.Repeat("0", 63)} {
		if _, err := c.Read(context.Background(), s.URL, invalid); err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("invalid checksum error = %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Read(ctx, s.URL, digest); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached canceled read = %v", err)
	}
}

func TestCancellationTimeoutAndWaitingCaller(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer s.Close()
	ca := trust(t, s)
	c := client(t, Options{CAFile: ca})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Read(ctx, s.URL, ""); done <- err }()
	<-started
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	if _, err := c.Read(waitCtx, s.URL, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller = %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation = %v", err)
	}
	c = client(t, Options{CAFile: ca, Timeout: 20 * time.Millisecond})
	if _, err := c.Read(context.Background(), s.URL, ""); err == nil || err.Error() != "source request timed out" {
		t.Fatalf("timeout = %v", err)
	}
}

func TestFailureCachedAndBodyErrorsSanitized(t *testing.T) {
	var hits atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "private-partial-body")
	}))
	defer s.Close()
	c := client(t, Options{CAFile: trust(t, s)})
	for range 2 {
		_, err := c.Read(context.Background(), s.URL, "")
		if err == nil || strings.Contains(err.Error(), "private-") || errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("body error = %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatal("failed request retried")
	}
}

func TestIsURLRecognizesMalformedSchemesBeforePathExpansion(t *testing.T) {
	for _, raw := range []string{"https:%zzPRIVATE", "https:%%PRIVATE", "https:PRIVATE@example.test/a", "https:/PRIVATE@example.test/a", "file:/private/path", "HTTPS:broken", "custom+scheme.1-value:broken", "://broken"} {
		if !IsURL(raw) {
			t.Errorf("URL-like source classified as local: %q", raw)
		}
		if err := ValidateURL(raw); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Errorf("malformed URL was not rejected safely: %v", err)
		}
	}
	for _, raw := range []string{"inventory.yaml", "~/inventory.yaml", "/tmp/inventory.yaml", "1invalid:path", "invalid_scheme:path", "relative/path:part"} {
		if IsURL(raw) {
			t.Errorf("local source classified as URL: %q", raw)
		}
	}
}

type sourceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sourceRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countedSourceBody struct {
	reader    io.Reader
	readCalls *atomic.Int32
	bytesRead *atomic.Int64
}

func (b countedSourceBody) Read(p []byte) (int, error) {
	b.readCalls.Add(1)
	n, err := b.reader.Read(p)
	b.bytesRead.Add(int64(n))
	return n, err
}
func (countedSourceBody) Close() error { return nil }

func TestConcurrentDistinctSourcesShareOneBoundedBodyBudget(t *testing.T) {
	const readers = 16
	const totalBudget = 9
	var calls atomic.Int32
	var bytesRead atomic.Int64
	arrived := make(chan struct{}, readers)
	c := client(t, Options{MaxTotalBytes: totalBudget})
	c.http.Transport = sourceRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		arrived <- struct{}{}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r, Body: countedSourceBody{reader: strings.NewReader(strings.Repeat("x", 128)), readCalls: &calls, bytesRead: &bytesRead}}, nil
	})
	// Hold the read gate until every distinct request has received a response.
	// No reader may reserve a stale copy of the same remaining allowance.
	c.bodyReads <- struct{}{}
	released := false
	defer func() {
		if !released {
			<-c.bodyReads
		}
	}()
	results := make(chan error, readers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, err := c.Read(ctx, "https://example.test/canceled", ""); results <- err }()
	<-arrived
	cancel()
	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reader waiting for budget did not preserve cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader waiting for budget blocked cancellation")
	}
	for i := range readers - 1 {
		go func() {
			_, err := c.Read(context.Background(), fmt.Sprintf("https://example.test/source-%d", i), "")
			results <- err
		}()
	}
	for range readers - 1 {
		<-arrived
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("body reads escaped the gate: %d", got)
	}
	<-c.bodyReads
	released = true
	for range readers - 1 {
		if err := <-results; err == nil || !strings.Contains(err.Error(), "total byte limit") {
			t.Fatalf("aggregate overflow did not fail safely: %v", err)
		}
	}
	if got := bytesRead.Load(); got != totalBudget+1 {
		t.Fatalf("aggregate read = %d, want one sentinel byte beyond %d", got, totalBudget)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("overflow caused repeated sentinel reads: %d", got)
	}
	c.mu.Lock()
	total := c.total
	c.mu.Unlock()
	if total != totalBudget+1 {
		t.Fatalf("aggregate accounting = %d", total)
	}
}

func TestRequestTimeoutIncludesWaitingForBodyBudget(t *testing.T) {
	c := client(t, Options{Timeout: 20 * time.Millisecond})
	var calls atomic.Int32
	var bytesRead atomic.Int64
	c.http.Transport = sourceRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r, Body: countedSourceBody{reader: strings.NewReader("ok"), readCalls: &calls, bytesRead: &bytesRead}}, nil
	})
	c.bodyReads <- struct{}{}
	released := false
	defer func() {
		if !released {
			<-c.bodyReads
		}
	}()
	done := make(chan error, 1)
	go func() { _, err := c.Read(context.Background(), "https://example.test/waiting", ""); done <- err }()
	select {
	case err := <-done:
		if err == nil || err.Error() != "source request timed out" {
			t.Fatalf("budget wait timeout = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request timeout did not include waiting for the body budget")
	}
	if calls.Load() != 0 {
		t.Fatal("expired request read its response body")
	}
	<-c.bodyReads
	released = true
	if doc, err := c.Read(context.Background(), "https://example.test/after", ""); err != nil || string(doc.Data) != "ok" {
		t.Fatalf("expired budget wait damaged the gate: %+v, %v", doc, err)
	}
}
