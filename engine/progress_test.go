package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/rpm"
)

func TestPreparationProgressPrecedesWorkAndPreservesPhaseOrder(t *testing.T) {
	var trace []string
	dir := tempDir(t)
	r := &inventory.Resolved{}
	for _, name := range []string{"first", "second"} {
		h := inventory.ResolvedHost{Name: name, Transport: inventory.Transport{Type: "local"}}
		for _, service := range []string{"app", "later"} {
			h.Services = append(h.Services, inventory.ResolvedService{
				Assignment: inventory.Assignment{Name: service, State: "present"},
				Definition: inventory.Service{
					Binaries: []inventory.Binary{{Type: "rpm", Package: "demo"}},
					Configs: []inventory.Config{{Path: filepath.Join(dir, name+service), Template: inventory.Template{
						Inline: inline(`{{call .render}}`),
					}}},
				},
			})
		}
		h.Vars = map[string]any{"render": func() string {
			trace = append(trace, "render "+name)
			return "prepared"
		}}
		r.Hosts = append(r.Hosts, h)
	}
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		trace = append(trace, name)
		if name == "dnf" {
			return []byte("4.20.0"), nil
		}
		return []byte("demo\t0:2.0-1.noarch"), nil
	}}}
	var events []PrepareEvent
	p, err := e.Diff(context.Background(), r, DiffOptions{Observer: func(event PrepareEvent) {
		events = append(events, event)
		trace = append(trace, event.Phase+" "+event.Host+"/"+event.Service)
		// Mutating this detached event cannot affect the engine's work or labels.
		event.Host, event.Service, event.Phase = "changed", "changed", "changed"
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"rendering first/app", "render first", "rendering first/later", "render first",
		"rendering second/app", "render second", "rendering second/later", "render second",
		"checking first/", "checking first/app", "dnf", "checking first/later",
		"checking second/", "checking second/app", "dnf", "checking second/later",
		"observing first/", "observing first/app", "rpm", "observing first/later", "rpm",
		"observing second/", "observing second/app", "rpm", "observing second/later", "rpm",
	}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("preparation order:\n got %q\nwant %q", trace, want)
	}
	if len(events) != 16 || p.Hosts[0].Name != "first" || p.Hosts[1].Name != "second" {
		t.Fatalf("events or plan labels changed: %v, %v", events, p.Hosts)
	}
	for _, h := range p.Hosts {
		if len(h.Tasks) != 2 || h.Tasks[0].File.Content != "prepared" || h.Tasks[1].File.Content != "prepared" {
			t.Fatalf("unexpected prepared tasks: %+v", h.Tasks)
		}
	}
}

func TestPreparationCancellationBeforeReportedWork(t *testing.T) {
	for _, stop := range []PrepareEvent{
		{Phase: "rendering", Host: "local", Service: "app"},
		{Phase: "checking", Host: "local"},
		{Phase: "checking", Host: "local", Service: "app"},
		{Phase: "observing", Host: "local"},
		{Phase: "observing", Host: "local", Service: "app"},
	} {
		t.Run(stop.Phase+"/"+stop.Service, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var commands []string
			e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				commands = append(commands, name)
				return []byte("4.20.0"), nil
			}}}
			r := resolved(inventory.Service{Binaries: []inventory.Binary{{Package: "demo"}}})
			var events []PrepareEvent
			p, err := e.Diff(ctx, r, DiffOptions{Observer: func(event PrepareEvent) {
				events = append(events, event)
				if event == stop {
					cancel()
				}
			}})
			if !errors.Is(err, context.Canceled) || p != nil || events[len(events)-1] != stop {
				t.Fatalf("plan=%v error=%v events=%v", p, err, events)
			}
			wantCalls := 0
			if stop.Phase == "observing" {
				wantCalls = 1
			}
			if len(commands) != wantCalls || wantCalls == 1 && commands[0] != "dnf" {
				t.Fatalf("unexpected work after cancellation: %v", commands)
			}
		})
	}
}

func TestPreparationCancellationBetweenTemplates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := resolved(inventory.Service{Configs: []inventory.Config{
		{Path: filepath.Join(tempDir(t), "first"), Template: inventory.Template{Inline: inline(`{{call .cancel}}`)}},
		{Path: filepath.Join(tempDir(t), "second"), Template: inventory.Template{Inline: inline(`{{call .unexpected}}`)}},
	}})
	r.Hosts[0].Vars = map[string]any{
		"cancel": func() string { cancel(); return "" },
		"unexpected": func() string {
			t.Error("next template rendered after cancellation")
			return ""
		},
	}
	var events []PrepareEvent
	p, err := (Local{}).Diff(ctx, r, DiffOptions{Observer: func(event PrepareEvent) { events = append(events, event) }})
	if !errors.Is(err, context.Canceled) || p != nil || len(events) != 1 || events[0].Phase != "rendering" {
		t.Fatalf("plan=%v error=%v events=%v", p, err, events)
	}
}

func TestPreparationCancellationBetweenObservations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observations := 0
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "dnf" {
			return []byte("4.20.0"), nil
		}
		observations++
		cancel()
		return nil, nil
	}}}
	r := resolved(inventory.Service{Binaries: []inventory.Binary{{Package: "first"}, {Package: "second"}}})
	p, err := e.Diff(ctx, r)
	if !errors.Is(err, context.Canceled) || p != nil || observations != 1 {
		t.Fatalf("plan=%v error=%v observations=%d", p, err, observations)
	}
}

func TestPreparationEventsExcludeSensitiveInputs(t *testing.T) {
	dir := tempDir(t)
	source := filepath.Join(dir, "private-source.yaml")
	if err := os.WriteFile(source, []byte("private-key: PRIVATE_SECRET_CONTENT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := resolved(inventory.Service{Configs: []inventory.Config{{
		Path:     filepath.Join(dir, "private-target"),
		Template: inventory.Template{Inline: inline(`PRIVATE_TEMPLATE {{secret "private-key" "private-source"}} {{.private}}`)},
	}}})
	r.SecretSources = map[string]inventory.SecretSource{"private-source": {Type: "yaml", File: source}}
	r.Hosts[0].Vars = map[string]any{"private": "PRIVATE_VARIABLE"}
	var events []PrepareEvent
	p, err := (Local{}).Diff(context.Background(), r, DiffOptions{Observer: func(event PrepareEvent) { events = append(events, event) }})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE_", "private-key", "private-source", "private-target", dir} {
		if strings.Contains(string(data), private) {
			t.Fatalf("preparation event exposed private input %q", private)
		}
	}
	file := p.Hosts[0].Tasks[0].File
	if !file.Sensitive || !strings.Contains(file.Content, "PRIVATE_SECRET_CONTENT") {
		t.Fatal("prepared sensitive content changed")
	}
}

func TestPreparationOptionalObserverAndCanceledContext(t *testing.T) {
	e := Local{RPM: rpm.Backend{Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "dnf" {
			return []byte("4.20.0"), nil
		}
		return []byte("demo\t0:2.0-1.noarch"), nil
	}}}
	r := resolved(inventory.Service{Binaries: []inventory.Binary{{Package: "demo"}}})
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	withOptions, err := e.Diff(context.Background(), r, DiffOptions{})
	if err != nil || !reflect.DeepEqual(p, withOptions) {
		t.Fatalf("nil observer changed plan: %v, %v", withOptions, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := e.Diff(ctx, r, DiffOptions{Observer: func(PrepareEvent) { t.Error("event after cancellation") }}); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("plan=%v error=%v", p, err)
	}
	if _, err := e.Diff(context.Background(), r, DiffOptions{}, DiffOptions{}); err == nil {
		t.Fatal("multiple options values accepted")
	}
}
