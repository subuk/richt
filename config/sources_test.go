package config_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/source"
)

func templateServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *source.Client) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := source.New(source.Options{CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	return server, client
}

func TestHTTPSPrepareSeparatesAcquisitionFromRendering(t *testing.T) {
	text := `{{.host}}={{secret "token"}}`
	var requests atomic.Int32
	server, client := templateServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(text))
	})
	digest := sha256.Sum256([]byte(text))
	c := inventory.Config{Path: "/example", Template: inventory.Template{URL: server.URL + "/template", SHA256: hex.EncodeToString(digest[:])}}
	lookup := lookupFunc(func(key, name string) (string, error) { return "example-private-value", nil })
	for _, host := range []string{"first", "second"} {
		f, err := config.PrepareContext(context.Background(), c, "present", map[string]any{"host": host}, client, lookup)
		if err != nil || f.Content != host+"=example-private-value" || !f.Sensitive {
			t.Fatalf("remote template did not render per host with sensitivity: %#v, %v", f, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("shared client fetched template %d times", requests.Load())
	}
}

func TestHTTPSPrepareAbsentSkipsSourceAndSecrets(t *testing.T) {
	f, err := config.PrepareContext(context.Background(), inventory.Config{Path: "/example", Sensitive: true, Template: inventory.Template{URL: "https://127.0.0.1:1/template"}}, "absent", nil, nil, lookupFunc(func(string, string) (string, error) {
		t.Fatal("absent remote template looked up a secret")
		return "", nil
	}))
	if err != nil || f.Content != "" || !f.Sensitive {
		t.Fatalf("absent preparation failed: %#v, %v", f, err)
	}
}

func TestHTTPSPrepareSourceAndRenderFailuresAreSafe(t *testing.T) {
	for _, tc := range []struct {
		name, text, digest string
		status             int
		sensitive          bool
		want               string
	}{
		{name: "status", status: 503, text: "EXAMPLE_PRIVATE_BODY", sensitive: true, want: "read sensitive template failed"},
		{name: "checksum", status: 200, text: "EXAMPLE_PRIVATE_BODY", digest: strings.Repeat("0", 64), sensitive: true, want: "read sensitive template failed"},
		{name: "parse", status: 200, text: `{{EXAMPLE_PRIVATE_EXPRESSION}}`, want: "render template: parsing failed"},
		{name: "execute", status: 200, text: `{{secret "token"}}{{index (secret "token") 999}}`, want: "render template: execution failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := templateServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.text))
			})
			digest := tc.digest
			if digest == "" {
				sum := sha256.Sum256([]byte(tc.text))
				digest = hex.EncodeToString(sum[:])
			}
			c := inventory.Config{Path: "/example", Sensitive: tc.sensitive, Template: inventory.Template{URL: server.URL + "/EXAMPLE_PRIVATE_PATH", SHA256: digest}}
			_, err := config.PrepareContext(context.Background(), c, "present", nil, client, lookupFunc(func(string, string) (string, error) { return "EXAMPLE_PRIVATE_VALUE", nil }))
			if err == nil || err.Error() != tc.want || strings.Contains(err.Error(), "EXAMPLE_PRIVATE") {
				t.Fatalf("unsafe or missing preparation diagnostic: %v", err)
			}
		})
	}
}

func TestHTTPSPreparePassesCancellationToSource(t *testing.T) {
	started := make(chan struct{})
	server, client := templateServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := config.PrepareContext(ctx, inventory.Config{Path: "/example", Template: inventory.Template{URL: server.URL, SHA256: strings.Repeat("1", 64)}}, "present", nil, client)
		result <- err
	}()
	select {
	case <-started:
		cancel()
	case <-ctx.Done():
		t.Fatal("template request did not start")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation was not preserved: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("source ignored cancellation")
	}
}

func TestHTTPSPrepareValidatesDeclarationBeforeReading(t *testing.T) {
	var requests atomic.Int32
	server, client := templateServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("content"))
	})
	for _, template := range []inventory.Template{
		{URL: server.URL},
		{URL: server.URL, SHA256: strings.Repeat("1", 64), Inline: ptr("content")},
		{URL: server.URL, SHA256: strings.Repeat("1", 64), File: "/missing/template"},
	} {
		if _, err := config.PrepareContext(context.Background(), inventory.Config{Path: "/example", Template: template}, "present", nil, client); err == nil {
			t.Fatal("invalid remote declaration was accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid declaration fetched a template")
	}
}
