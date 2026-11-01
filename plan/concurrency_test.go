package plan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subuk/richt/plan"
)

type hostExecutor struct {
	host    string
	run     func(context.Context, string, string) error
	resolve func(string, plan.Task) ([]plan.Action, error)
}

func (e hostExecutor) ForHost(h plan.Host) (plan.Executor, error) {
	e.host = h.Name
	return e, nil
}
func (e hostExecutor) Resolve(t plan.Task) ([]plan.Action, error) {
	if e.resolve != nil {
		return e.resolve(e.host, t)
	}
	return []plan.Action{{Kind: t.ID, Description: []string{e.host + "/" + t.ID}}}, nil
}
func (e hostExecutor) Execute(ctx context.Context, a plan.Action) error {
	return e.run(ctx, e.host, a.Kind)
}

func manyHosts(count int) *plan.Plan {
	p := &plan.Plan{Version: plan.Version}
	for i := range count {
		p.Hosts = append(p.Hosts, plan.Host{Name: fmt.Sprintf("host%02d", i), Transport: "local", Tasks: []plan.Task{
			{ID: "first", Progress: plan.Progress{Status: "pending"}},
			{ID: "second", Progress: plan.Progress{Status: "pending"}},
		}})
	}
	return p
}

func receive(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for action")
		return ""
	}
}

func TestHostConcurrencyLimitsAndSequentialTasks(t *testing.T) {
	for _, limit := range []int{1, 2, plan.DefaultHostConcurrency} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			p := manyHosts(24)
			started := make(chan string, len(p.Hosts))
			release := make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			var mu sync.Mutex
			active, maxActive := 0, 0
			calls := map[string][]string{}
			e := hostExecutor{run: func(_ context.Context, host, task string) error {
				mu.Lock()
				active++
				if active > maxActive {
					maxActive = active
				}
				calls[host] = append(calls[host], task)
				mu.Unlock()
				if task == "first" {
					started <- host
					<-release
				}
				mu.Lock()
				active--
				mu.Unlock()
				return nil
			}}
			type outcome struct {
				result plan.Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				var r plan.Result
				var err error
				if limit == plan.DefaultHostConcurrency {
					r, err = plan.Apply(context.Background(), p, e, nil)
				} else {
					r, err = plan.Apply(context.Background(), p, e, nil, plan.ApplyOptions{HostConcurrency: limit})
				}
				done <- outcome{r, err}
			}()
			for range limit {
				receive(t, started)
			}
			select {
			case host := <-started:
				t.Fatalf("host %s exceeded concurrency limit %d", host, limit)
			case <-time.After(25 * time.Millisecond):
			}
			once.Do(func() { close(release) })
			out := <-done
			if out.err != nil {
				t.Fatal(out.err)
			}
			if maxActive != limit {
				t.Fatalf("maximum active %d, want %d", maxActive, limit)
			}
			for i, h := range p.Hosts {
				if strings.Join(calls[h.Name], ",") != "first,second" {
					t.Fatalf("host %s calls %v", h.Name, calls[h.Name])
				}
				if out.result.Hosts[i].Name != h.Name {
					t.Fatal("nondeterministic result order")
				}
			}
		})
	}
}

func TestConcurrentProgressSnapshotsAndHostFailureIsolation(t *testing.T) {
	p := manyHosts(25)
	var snapshots int
	var previous *plan.Plan
	e := hostExecutor{run: func(_ context.Context, host, task string) error {
		if host == "host00" && task == "first" {
			return errors.New("authentication failed")
		}
		return nil
	}}
	r, err := plan.Apply(context.Background(), p, e, func(snapshot *plan.Plan) error {
		data, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		var current plan.Plan
		if err := json.Unmarshal(data, &current); err != nil {
			return err
		}
		if err := current.Validate(); err != nil {
			return err
		}
		if previous != nil {
			for hi, h := range previous.Hosts {
				for ti, task := range h.Tasks {
					if task.Progress.Status == "success" && current.Hosts[hi].Tasks[ti].Progress.Status != "success" {
						return errors.New("lost successful progress")
					}
				}
			}
		}
		previous = &current
		snapshots++
		return nil
	}, plan.ApplyOptions{HostConcurrency: 3})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error %v", err)
	}
	if r.Hosts[0].Tasks[0].Status != "error" || r.Hosts[0].Tasks[1].Status != "blocked" {
		t.Fatal(r.Hosts[0])
	}
	for _, h := range r.Hosts[1:] {
		for _, task := range h.Tasks {
			if task.Status != "success" {
				t.Fatal(h)
			}
		}
	}
	if snapshots != 98 {
		t.Fatalf("snapshots %d, want 98", snapshots)
	}
}

func TestPersistenceFailureAwaitsActiveActionsAndStopsAdmissions(t *testing.T) {
	p := manyHosts(3)
	aStarted := make(chan struct{})
	failedSave := make(chan struct{})
	var mu sync.Mutex
	var calls []string
	e := hostExecutor{run: func(_ context.Context, host, task string) error {
		mu.Lock()
		calls = append(calls, host+"/"+task)
		mu.Unlock()
		switch host {
		case "host00":
			close(aStarted)
			<-failedSave
		case "host01":
			<-aStarted
		}
		return nil
	}}
	saveFailed := false
	r, err := plan.Apply(context.Background(), p, e, func(snapshot *plan.Plan) error {
		if !saveFailed && snapshot.Hosts[1].Tasks[0].Progress.Status == "success" {
			saveFailed = true
			close(failedSave)
			return errors.New("disk full")
		}
		return nil
	}, plan.ApplyOptions{HostConcurrency: 2})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("unexpected actions %v", calls)
	}
	for hi, h := range r.Hosts {
		for ti, task := range h.Tasks {
			want := "blocked"
			if hi < 2 && ti == 0 {
				want = "success"
			}
			if task.Status != want {
				t.Fatalf("result %+v", r)
			}
		}
	}
	if p.Hosts[0].Tasks[0].Progress.Status != "success" {
		t.Fatal("active action completion was lost")
	}
}

func TestCancellationStopsQueuedHosts(t *testing.T) {
	p := manyHosts(3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 1)
	e := hostExecutor{run: func(ctx context.Context, host, task string) error {
		started <- host
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}}
	r, err := plan.Apply(ctx, p, e, nil, plan.ApplyOptions{HostConcurrency: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
	if len(started) != 1 {
		t.Fatal("queued action ran")
	}
	if r.Hosts[0].Tasks[0].Status != "error" || r.Hosts[2].Tasks[0].Status != "blocked" {
		t.Fatal(r)
	}
}

func TestHostBindingAndPreflight(t *testing.T) {
	p := manyHosts(2)
	p.Hosts[1].Transport = "ssh"
	p.Hosts[1].Connection = &plan.Connection{Host: "alias", User: "alice", Port: 2222, Sudo: true}
	e := hostExecutor{run: func(context.Context, string, string) error { t.Fatal("preflight executed an action"); return nil }}
	var output bytes.Buffer
	if err := plan.Display(&output, p, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "host00/first") || !strings.Contains(output.String(), "host01/first") {
		t.Fatal(output.String())
	}
	for _, value := range []string{"host=alias", "user=alice", "port=2222", "sudo=true", "current client configuration"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("display missing %q: %s", value, output.String())
		}
	}
	empty := manyHosts(1)
	empty.Hosts[0].Connection = &plan.Connection{Sudo: true}
	empty.Hosts[0].Tasks = nil
	output.Reset()
	if err := plan.Display(&output, empty, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "sudo=true") || !strings.Contains(output.String(), "No changes") {
		t.Fatal(output.String())
	}
	e.resolve = func(host string, task plan.Task) ([]plan.Action, error) {
		if host == "host01" {
			return nil, errors.New("invalid operation")
		}
		return []plan.Action{{Kind: task.ID}}, nil
	}
	if _, err := plan.Apply(context.Background(), p, e, nil); err == nil {
		t.Fatal("invalid later host accepted")
	}
	if _, err := plan.Apply(context.Background(), p, &executor{}, nil); err == nil {
		t.Fatal("legacy executor accepted SSH")
	}
	p.Hosts[1].Transport = "local"
	p.Hosts[1].Connection = &plan.Connection{Sudo: true}
	if _, err := plan.Apply(context.Background(), p, &executor{}, nil); err == nil {
		t.Fatal("legacy executor ignored sudo")
	}
	for _, limit := range []int{-1, 0} {
		if _, err := plan.Apply(context.Background(), manyHosts(1), &executor{}, nil, plan.ApplyOptions{HostConcurrency: limit}); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
}

func TestSavedConnectionNamesAndLegacyCompatibility(t *testing.T) {
	p := manyHosts(1)
	p.Hosts[0].Transport = "ssh"
	p.Hosts[0].Connection = &plan.Connection{Host: "alias", User: "alice", Port: 2222, Sudo: true}
	p.Hosts[0].Tasks[0].File = &plan.FileChange{Owner: "app", Group: "staff"}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := plan.Save(path, p); err != nil {
		t.Fatal(err)
	}
	s, err := plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Plan.Hosts[0].Connection.User != "alice" || s.Plan.Hosts[0].Tasks[0].File.Owner != "app" {
		t.Fatal(s.Plan)
	}
	s.Close()
	legacy := `{"version":"1.0","hosts":[{"name":"old","transport":"local","tasks":[{"id":"file","service":"app","component":"config.main","operation":"file","file":{"path":"/tmp/file","state":"present","uid":42,"gid":43},"progress":{"status":"pending"}}]}]}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = plan.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if *s.Plan.Hosts[0].Tasks[0].File.UID != 42 {
		t.Fatal("numeric legacy owner lost")
	}
	s.Close()
	p.Version = "1.0"
	if err := p.Validate(); err == nil {
		t.Fatal("legacy version accepted new connection")
	}
	p.Hosts[0].Transport, p.Hosts[0].Connection = "local", nil
	if err := p.Validate(); err == nil {
		t.Fatal("legacy version accepted ownership names")
	}
}

func TestInvalidSavedConnections(t *testing.T) {
	for _, connection := range []plan.Connection{
		{}, {Host: "-option"}, {Host: "user@host"}, {Host: "ssh://host"},
		{Host: "white space"}, {Host: "line\nhost"}, {Host: "host", User: "-option"},
		{Host: "host", User: "two users"}, {Host: "host", Port: -1}, {Host: "host", Port: 65536},
	} {
		p := manyHosts(1)
		p.Hosts[0].Transport, p.Hosts[0].Connection = "ssh", &connection
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted invalid connection %+v", connection)
		}
	}
}
