package engine

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
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/source"
)

func httpsTemplates(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *source.Client) {
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

func TestHTTPSTemplateSnapshotSharedAcrossConfigsUnitsDropInsAndHosts(t *testing.T) {
	var requests atomic.Int32
	const text = "[Service]\nEnvironment=HOST={{.host}}\nExecStart={{secret \"command\"}}\n"
	server, client := httpsTemplates(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) > 1 {
			_, _ = w.Write([]byte("changed after first acquisition"))
			return
		}
		_, _ = w.Write([]byte(text))
	})
	secretFile := filepath.Join(tempDir(t), "secrets.yaml")
	if err := os.WriteFile(secretFile, []byte("command: /bin/true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(text))
	template := inventory.Template{URL: server.URL + "/shared-template", SHA256: hex.EncodeToString(sum[:])}
	r := resolved(inventory.Service{
		Configs: []inventory.Config{{Name: "main", Path: "/tmp/richt-remote-template-test", Template: template}},
		Processes: []inventory.Process{{Name: "main", Unit: "richt-remote-template-test.service", State: "stopped",
			UnitFile:  &inventory.Config{Path: "/etc/systemd/system/richt-remote-template-test.service", Template: template},
			Overrides: map[string]inventory.Config{"20-test.conf": {Path: "/etc/systemd/system/richt-remote-template-test.service.d/20-test.conf", Template: template}},
		}},
	})
	r.SecretSources = map[string]inventory.SecretSource{"default": {Type: "yaml", File: secretFile}}
	r.Hosts[0].Vars = map[string]any{"host": "first"}
	h := r.Hosts[0]
	h.Name, h.Vars = "second", map[string]any{"host": "second"}
	r.Hosts = append(r.Hosts, h)
	e := Local{Files: &config.Target{
		Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("-\n-\n"), nil },
		RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
			return fileObservation("absent", ""), nil
		},
	}, Systemd: fakeSystemd("inactive", false)}
	e.Systemd.Glob = func(context.Context, string) ([]string, error) { return nil, nil }
	p, err := e.Diff(context.Background(), r, DiffOptions{Sources: client})
	if err != nil {
		t.Fatal(err)
	}
	for i, host := range p.Hosts {
		wantHost := []string{"first", "second"}[i]
		files := 0
		for _, task := range host.Tasks {
			if f := task.File; f != nil {
				files++
				if !f.Sensitive || !strings.Contains(f.Content, "HOST="+wantHost+"\n") || !strings.Contains(f.Content, "ExecStart=/bin/true") {
					t.Fatalf("remote snapshot, host variables, or sensitivity lost: %#v", f)
				}
			}
		}
		if files != 3 {
			t.Fatalf("expected config, unit and drop-in; got %d files", files)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("template snapshot fetched %d times", requests.Load())
	}
}

func TestHTTPSTemplateFailuresPrecedeAllHostObservation(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, client := httpsTemplates(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("new"))
			})
			r := resolved(inventory.Service{Configs: []inventory.Config{{Path: "/etc/richt-template-test", Template: inventory.Template{Inline: inline("valid")}}}})
			h := r.Hosts[0]
			h.Name = "second"
			h.Services = []inventory.ResolvedService{{Assignment: inventory.Assignment{Name: "bad", State: "present"}, Definition: inventory.Service{Configs: []inventory.Config{{Path: "/etc/richt-template-test", Template: inventory.Template{URL: server.URL, SHA256: strings.Repeat("0", 64)}}}}}}
			r.Hosts = append(r.Hosts, h)
			for i := range r.Hosts {
				r.Hosts[i].Transport = inventory.Transport{Type: "ssh", Host: "unused"}
			}
			calls := 0
			e := Local{Connect: func(inventory.Transport) (config.Target, error) {
				calls++
				return config.Target{}, errors.New("unexpected host observation")
			}}
			p, err := e.Diff(context.Background(), r, DiffOptions{Sources: client})
			if err == nil || p != nil || calls != 0 {
				t.Fatalf("template source failure did not precede host binding: %v, %d", err, calls)
			}
		})
	}
}

func TestHTTPSAbsentTemplatesDoNotReadSources(t *testing.T) {
	var requests atomic.Int32
	server, client := httpsTemplates(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{{secret "missing"}}`))
	})
	r := resolved(inventory.Service{Configs: []inventory.Config{{Path: "/etc/richt-template-test", Template: inventory.Template{URL: server.URL, SHA256: strings.Repeat("1", 64)}}}})
	r.Hosts[0].Services[0].State = "absent"
	r.SecretSources = map[string]inventory.SecretSource{"default": {Type: "yaml", File: "/missing-secret-source"}}
	e := Local{Files: &config.Target{
		Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("-\n-\n"), nil },
		RunInput: func(context.Context, []byte, string, ...string) ([]byte, error) {
			return fileObservation("absent", ""), nil
		},
	}}
	if _, err := e.Diff(context.Background(), r, DiffOptions{Sources: client}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Diff(context.Background(), &inventory.Resolved{}, DiffOptions{Sources: client}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("absent or unselected templates were fetched")
	}
}

func TestHTTPSTemplateCancellationPrecedesHostObservation(t *testing.T) {
	started := make(chan struct{})
	server, client := httpsTemplates(t, func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	r := resolved(inventory.Service{Configs: []inventory.Config{{Path: "/etc/richt-template-test", Template: inventory.Template{URL: server.URL, SHA256: strings.Repeat("1", 64)}}}})
	r.Hosts[0].Transport = inventory.Transport{Type: "ssh", Host: "unused"}
	var calls atomic.Int32
	e := Local{Connect: func(inventory.Transport) (config.Target, error) {
		calls.Add(1)
		return config.Target{}, errors.New("unexpected host observation")
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := e.Diff(ctx, r, DiffOptions{Sources: client})
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
		if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
			t.Fatalf("cancellation did not stop preparation: %v, %d", err, calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("template preparation ignored cancellation")
	}
}

func TestSavedHTTPSTemplatePlanAppliesAfterSourceDisappears(t *testing.T) {
	const text = "hello {{.host}}\n"
	server, client := httpsTemplates(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(text))
	})
	dir := tempDir(t)
	target := filepath.Join(dir, "target")
	sum := sha256.Sum256([]byte(text))
	r := resolved(inventory.Service{Configs: []inventory.Config{{Path: target, Template: inventory.Template{URL: server.URL + "/template", SHA256: hex.EncodeToString(sum[:])}}}})
	r.Hosts[0].Vars = map[string]any{"host": "frozen"}
	p, err := (Local{}).Diff(context.Background(), r, DiffOptions{Sources: client})
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "plan.json")
	if err := plan.Save(filename, p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil || strings.Contains(string(data), server.URL) || p.Version != "1.8" {
		t.Fatalf("plan persisted source inputs or changed format: %v", err)
	}
	server.Close()
	r.Hosts = nil
	store, err := plan.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := plan.Apply(context.Background(), store.Plan, Local{}, store.Save); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "hello frozen\n" {
		t.Fatalf("saved remote template bytes changed: %q, %v", data, err)
	}
}
