// Package source reads bounded, authenticated HTTPS preparation inputs.
package source

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Document is a detached snapshot and the URL of its final response.
type Document struct {
	Data []byte
	URL  string
}

// Auth supplies credentials only within an exact HTTPS origin and path scope.
// A longer matching PathPrefix takes precedence over a shorter one.
type Auth struct {
	Origin      string
	PathPrefix  string
	BearerToken string
	Username    string
	Password    string
}

// Options configures one preparation's source client. Zero limits use defaults.
type Options struct {
	CAFile        string
	Auth          []Auth
	Timeout       time.Duration
	MaxBytes      int64
	MaxTotalBytes int64
	MaxDocuments  int
	MaxRedirects  int
}

type entry struct {
	done chan struct{}
	doc  Document
	err  error
}

// Client retains one snapshot or failure for each requested URL. It is safe for
// concurrent use and should be created separately for each preparation.
type Client struct {
	options   Options
	http      *http.Client
	mu        sync.Mutex
	cache     map[string]*entry
	total     int64
	bodyReads chan struct{}
}

// New constructs a client with verified TLS and optional additional trust roots.
func New(options Options) (*Client, error) {
	if options.Timeout < 0 || options.MaxBytes < 0 || options.MaxTotalBytes < 0 || options.MaxDocuments < 0 || options.MaxRedirects < 0 {
		return nil, errors.New("source limits must be positive")
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 10 << 20
	}
	if options.MaxTotalBytes == 0 {
		options.MaxTotalBytes = 100 << 20
	}
	if options.MaxDocuments == 0 {
		options.MaxDocuments = 256
	}
	if options.MaxRedirects == 0 {
		options.MaxRedirects = 5
	}
	options.Auth = append([]Auth(nil), options.Auth...)
	scopes := make(map[string]bool)
	for i := range options.Auth {
		a := &options.Auth[i]
		u, err := parseURL(a.Origin)
		if err != nil || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("invalid source authentication origin")
		}
		a.Origin = origin(u)
		if a.PathPrefix == "" {
			a.PathPrefix = "/"
		}
		if !strings.HasPrefix(a.PathPrefix, "/") || strings.ContainsAny(a.PathPrefix, "?#\\%") || hasControl(a.PathPrefix) {
			return nil, errors.New("invalid source authentication path prefix")
		}
		a.PathPrefix = path.Clean(a.PathPrefix)
		if hasControl(a.BearerToken) || hasControl(a.Username) || hasControl(a.Password) || strings.Contains(a.Username, ":") {
			return nil, errors.New("invalid source authentication credentials")
		}
		if (a.BearerToken != "" && (a.Username != "" || a.Password != "")) || (a.BearerToken == "" && a.Username == "") {
			return nil, errors.New("source authentication requires bearer or basic credentials")
		}
		key := a.Origin + a.PathPrefix
		if scopes[key] {
			return nil, errors.New("duplicate source authentication scope")
		}
		scopes[key] = true
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if options.CAFile != "" {
		pem, err := os.ReadFile(options.CAFile)
		if err != nil {
			return nil, errors.New("cannot read source CA file")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("cannot load source TLS trust roots")
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid source CA file")
		}
		tlsConfig.RootCAs = roots
	}
	c := &Client{options: options, cache: make(map[string]*entry), bodyReads: make(chan struct{}, 1)}
	// Fresh connections avoid the transport's transparent retries on reused
	// connections. No cookie jar or ambient application credentials are used.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlsConfig,
		DisableKeepAlives:   true,
		DialContext:         (&net.Dialer{Timeout: options.Timeout, KeepAlive: -1}).DialContext,
		TLSHandshakeTimeout: options.Timeout,
	}
	c.http = &http.Client{
		Transport: transport, Timeout: options.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > options.MaxRedirects {
				return errors.New("source redirect limit exceeded")
			}
			u, err := parseURL(req.URL.String())
			if err != nil {
				return errors.New("source redirect URL is invalid")
			}
			req.URL = u
			// net/http normally copies headers, and may add a Referer. Rebuild
			// them for every destination, including same-origin redirects.
			req.Header = make(http.Header)
			c.authorize(req)
			return nil
		},
	}
	return c, nil
}

// IsURL classifies source locations before local path expansion. It recognizes
// URI scheme syntax even when the rest of the URL is malformed; it does not
// validate URLs. Call ValidateURL before using a classified URL.
func IsURL(raw string) bool {
	if strings.Contains(raw, "://") {
		return true
	}
	colon := strings.IndexByte(raw, ':')
	if colon < 1 {
		return false
	}
	for i, r := range raw[:colon] {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || !(r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.')) {
			return false
		}
	}
	return true
}

// ValidateURL accepts absolute HTTPS document URLs without user information,
// query strings, fragments, or ambiguous authority syntax.
func ValidateURL(raw string) error {
	_, err := parseURL(raw)
	return err
}

// ValidateSHA256 validates an optional SHA-256 digest without quoting input.
func ValidateSHA256(digest string) error {
	if digest == "" {
		return nil
	}
	if len(digest) != 64 {
		return errors.New("source SHA-256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("source SHA-256 must contain 64 hexadecimal characters")
	}
	return nil
}

// Resolve resolves a relative document reference against a final HTTPS URL.
// Absolute HTTPS references are allowed; scheme-relative references are not.
func Resolve(baseURL, reference string) (string, error) {
	base, err := parseURL(baseURL)
	if err != nil {
		return "", err
	}
	if hasControl(reference) || strings.ContainsAny(reference, "?#\\") || strings.HasPrefix(reference, "//") {
		return "", errors.New("invalid source reference")
	}
	ref, err := url.Parse(reference)
	if err != nil || ref.User != nil || ref.Opaque != "" || (ref.Scheme != "" && !strings.EqualFold(ref.Scheme, "https")) {
		return "", errors.New("invalid source reference")
	}
	u, err := parseURL(base.ResolveReference(ref).String())
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func parseURL(raw string) (*url.URL, error) {
	invalid := func() (*url.URL, error) {
		return nil, errors.New("source URL must be absolute HTTPS without credentials, query, or fragment")
	}
	if hasControl(raw) || strings.ContainsAny(raw, "?#\\") {
		return invalid()
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.User != nil || u.Host == "" || u.Hostname() == "" {
		return invalid()
	}
	host := strings.ToLower(u.Hostname())
	if hasControl(u.Path) || strings.Contains(u.Path, "\\") || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") || strings.ContainsAny(host, "% /\\") {
		return invalid()
	}
	if strings.Contains(host, ":") {
		if net.ParseIP(host) == nil || !strings.HasPrefix(u.Host, "[") {
			return invalid()
		}
	} else {
		for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return invalid()
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
					return invalid()
				}
			}
		}
		if len(host) > 253 {
			return invalid()
		}
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return invalid()
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return invalid()
		}
		port = strconv.Itoa(n)
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Scheme = "https"
	u.Host = host
	if port != "" && port != "443" {
		u.Host += ":" + port
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func origin(u *url.URL) string { return "https://" + u.Host }

func (c *Client) authorize(req *http.Request) {
	var best *Auth
	p := req.URL.Path
	// Nested escaping has server-dependent interpretations. Never attach
	// credentials to such paths. Dot segments must remain inside the scope
	// both before and after normalization.
	if strings.Contains(p, "%") {
		return
	}
	clean := path.Clean(p)
	matches := func(prefix, candidate string) bool {
		return prefix == "/" || candidate == prefix || strings.HasPrefix(candidate, prefix+"/")
	}
	for i := range c.options.Auth {
		a := &c.options.Auth[i]
		if a.Origin == origin(req.URL) && matches(a.PathPrefix, p) && matches(a.PathPrefix, clean) && (best == nil || len(a.PathPrefix) > len(best.PathPrefix)) {
			best = a
		}
	}
	if best != nil {
		if best.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+best.BearerToken)
		} else {
			req.SetBasicAuth(best.Username, best.Password)
		}
	}
}

// Read returns the first downloaded snapshot for rawURL and checks the optional
// digest against that snapshot even when another caller already fetched it.
func (c *Client) Read(ctx context.Context, rawURL, digest string) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	if err := ValidateSHA256(digest); err != nil {
		return Document{}, err
	}
	u, err := parseURL(rawURL)
	if err != nil {
		return Document{}, err
	}
	key := u.String()
	c.mu.Lock()
	e, exists := c.cache[key]
	if !exists {
		if len(c.cache) >= c.options.MaxDocuments {
			c.mu.Unlock()
			return Document{}, errors.New("source document limit exceeded")
		}
		if c.total >= c.options.MaxTotalBytes {
			c.mu.Unlock()
			return Document{}, errors.New("source total byte limit exceeded")
		}
		e = &entry{done: make(chan struct{})}
		c.cache[key] = e
	}
	c.mu.Unlock()
	if !exists {
		e.doc, e.err = c.fetch(ctx, u)
		close(e.done)
	}
	select {
	case <-ctx.Done():
		return Document{}, ctx.Err()
	case <-e.done:
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	if e.err != nil {
		return Document{}, e.err
	}
	if digest != "" {
		sum := sha256.Sum256(e.doc.Data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), digest) {
			return Document{}, errors.New("source SHA-256 mismatch")
		}
	}
	return Document{Data: append([]byte(nil), e.doc.Data...), URL: e.doc.URL}, nil
}

func (c *Client) fetch(ctx context.Context, u *url.URL) (Document, error) {
	callerContext := ctx
	ctx, cancel := context.WithTimeout(ctx, c.options.Timeout)
	defer cancel()
	contextError := func() error {
		if err := callerContext.Err(); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return errors.New("source request timed out")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Document{}, errors.New("cannot create source request")
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		if err := contextError(); err != nil {
			return Document{}, err
		}
		// Never wrap net/http errors: their URL and underlying diagnostics may
		// include credentials or signed redirect locations.
		if errors.Is(err, context.DeadlineExceeded) {
			return Document{}, errors.New("source request timed out")
		}
		return Document{}, errors.New("source HTTPS request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Document{}, fmt.Errorf("source returned HTTP status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return Document{}, errors.New("unsupported source content encoding")
	}
	var data []byte
	buffer := make([]byte, 32<<10)
	for {
		n, readErr, limitErr := c.readBodyChunk(ctx, resp.Body, buffer, int64(len(data)))
		if limitErr != nil {
			return Document{}, limitErr
		}
		if n > 0 {
			data = append(data, buffer[:n]...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if err := contextError(); err != nil {
				return Document{}, err
			}
			if errors.Is(readErr, context.DeadlineExceeded) {
				return Document{}, errors.New("source request timed out")
			}
			return Document{}, errors.New("cannot read source response")
		}
	}
	if err := contextError(); err != nil {
		return Document{}, err
	}
	return Document{Data: data, URL: resp.Request.URL.String()}, nil
}

// readBodyChunk makes the shared budget calculation, bounded read, and byte
// accounting indivisible. The gate is separate from the cache mutex, so a slow
// response never prevents cache lookup or cancellation of a waiting reader.
func (c *Client) readBodyChunk(ctx context.Context, body io.Reader, buffer []byte, documentBytes int64) (n int, readErr, limitErr error) {
	select {
	case c.bodyReads <- struct{}{}:
		defer func() { <-c.bodyReads }()
	case <-ctx.Done():
		return 0, ctx.Err(), nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err, nil
	}
	c.mu.Lock()
	remainingTotal := c.options.MaxTotalBytes - c.total
	c.mu.Unlock()
	if remainingTotal < 0 {
		return 0, nil, errors.New("source total byte limit exceeded")
	}
	chunk := buffer
	remainingDocument := c.options.MaxBytes - documentBytes
	if remainingDocument < int64(len(chunk)) {
		chunk = chunk[:remainingDocument+1]
	}
	if remainingTotal < int64(len(chunk)) {
		chunk = chunk[:remainingTotal+1]
	}
	n, readErr = body.Read(chunk)
	if n > 0 {
		c.mu.Lock()
		c.total += int64(n)
		overTotal := c.total > c.options.MaxTotalBytes
		c.mu.Unlock()
		if overTotal {
			return n, readErr, errors.New("source total byte limit exceeded")
		}
		if documentBytes+int64(n) > c.options.MaxBytes {
			return n, readErr, errors.New("source document byte limit exceeded")
		}
	}
	return n, readErr, nil
}
