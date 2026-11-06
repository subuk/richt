package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subuk/richt/engine"
	"github.com/subuk/richt/plan"
)

type progressBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *progressBuffer) Write(v []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(v)
}
func (b *progressBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

type progressExecutor struct {
	started chan struct{}
	release <-chan struct{}
}

func (e progressExecutor) Resolve(t plan.Task) ([]plan.Action, error) {
	return []plan.Action{{Kind: t.ID}}, nil
}
func (e progressExecutor) Execute(ctx context.Context, a plan.Action) error {
	close(e.started)
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestProgressWritesWhileApplyIsStillRunning(t *testing.T) {
	var out progressBuffer
	start := time.Unix(1000, 0)
	p := newProgressWithSettings(&out, "plain", "Applying", progressSettings{now: func() time.Time { return start }})
	defer p.stop()
	input := &plan.Plan{Version: plan.Version, Hosts: []plan.Host{{Name: "host", Transport: "local", Tasks: []plan.Task{{ID: "service/config/0", Progress: plan.Progress{Status: "pending"}}}}}}
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := plan.Apply(context.Background(), input, progressExecutor{entered, release}, nil, plan.ApplyOptions{HostConcurrency: 1, Observer: p.apply})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not start")
	}
	if text := out.String(); !strings.Contains(text, "Started host=host task=service/config/0") {
		t.Fatalf("missing live event: %s", text)
	}
	select {
	case err := <-done:
		t.Fatalf("apply completed before release: %v", err)
	default:
	}
	p.refresh(start.Add(30 * time.Second))
	if text := out.String(); !strings.Contains(text, "active=1") || !strings.Contains(text, "elapsed=30s") {
		t.Fatalf("missing heartbeat: %s", text)
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := p.stop(); err != nil {
		t.Fatal(err)
	}
	if text := out.String(); !strings.Contains(text, "Completed host=host") || !strings.Contains(text, "Apply finished") {
		t.Fatal(text)
	}
}

func TestProgressModesAndSafeMetadata(t *testing.T) {
	for _, mode := range []string{"auto", "plain", "off"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			p := newProgressWithSettings(&out, mode, "Preparing", progressSettings{})
			p.prepare(engine.PrepareEvent{Phase: "rendering", Host: "host\n\x1b[31m", Service: "service"})
			p.apply(plan.Event{Kind: plan.EventTaskFailed, Host: "host", TaskID: "service/config/0", Error: "sensitive file operation failed"})
			if err := p.stop(); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if mode == "off" {
				if text != "" {
					t.Fatalf("off wrote %q", text)
				}
				return
			}
			if !strings.Contains(text, "Preparing: rendering") || !strings.Contains(text, "Failed host=host") || !strings.Contains(text, "sensitive file operation failed") || strings.Contains(text, "\x1b") {
				t.Fatalf("unsafe or missing metadata: %q", text)
			}
		})
	}
	var out bytes.Buffer
	p := newProgressWithSettings(&out, "plain", "Applying", progressSettings{terminal: true})
	p.stop()
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("plain used terminal escapes")
	}
}

func TestTTYProgressClipsAndKeepsCompletionHistory(t *testing.T) {
	var out bytes.Buffer
	start := time.Unix(1000, 0)
	p := newProgressWithSettings(&out, "auto", "Applying", progressSettings{now: func() time.Time { return start }, terminal: true, size: func() (int, int) { return 52, 6 }})
	p.apply(plan.Event{Kind: plan.EventApplyStarted, Total: 10})
	for _, host := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccc", "dddddddddddddddddddd", "世界世界世界世界世界世界世界"} {
		p.apply(plan.Event{Kind: plan.EventTaskStarted, Host: host, TaskID: "service/config/0"})
	}
	p.refresh(start.Add(61 * time.Second))
	if p.lines > 4 {
		t.Fatalf("dashboard too tall: %d", p.lines)
	}
	p.apply(plan.Event{Kind: plan.EventTaskSucceeded, Host: "host", TaskID: "task"})
	if err := p.stop(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Completed host=host task=task") || !strings.Contains(text, "Apply 1m1s") || !strings.Contains(text, "more active task") {
		t.Fatalf("missing live/completion output: %q", text)
	}
	ansi := regexp.MustCompile("\x1b\\[[0-9]*[AJ]")
	for _, line := range strings.Split(ansi.ReplaceAllString(text, ""), "\n") {
		width := 0
		for _, r := range strings.ReplaceAll(line, "\r", "") {
			width += progressRuneWidth(r)
		}
		if width > 51 {
			t.Fatalf("wrapped line width=%d: %q", width, line)
		}
	}
	if p.lines != 0 {
		t.Fatal("stop left dashboard on terminal")
	}
}

func TestProgressHeartbeatUsesInjectableTick(t *testing.T) {
	var out progressBuffer
	ticks := make(chan time.Time)
	start := time.Unix(1000, 0)
	p := newProgressWithSettings(&out, "plain", "Preparing", progressSettings{now: func() time.Time { return start }, ticks: ticks})
	defer p.stop()
	ticks <- start.Add(29 * time.Second)
	p.mu.Lock()
	p.mu.Unlock()
	if strings.Contains(out.String(), "elapsed=") {
		t.Fatal("heartbeat before interval")
	}
	ticks <- start.Add(30 * time.Second)
	deadline := time.After(5 * time.Second)
	for !strings.Contains(out.String(), "elapsed=30s") {
		select {
		case <-deadline:
			t.Fatal("tick did not produce heartbeat")
		default:
		}
		// Let the renderer process the injected tick without waiting real seconds.
		time.Sleep(time.Millisecond)
	}
}

type failedProgressWriter struct{}

func (failedProgressWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestProgressWriteFailureIsReported(t *testing.T) {
	p := newProgressWithSettings(failedProgressWriter{}, "plain", "Applying", progressSettings{})
	if err := p.stop(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error lost: %v", err)
	}
}

func TestProgressNamedComponentsPersistenceAndCancellation(t *testing.T) {
	var out bytes.Buffer
	p := newProgressWithSettings(&out, "plain", "Applying", progressSettings{})
	event := plan.Event{Kind: plan.EventTaskStarted, Host: "host", Service: "web", Component: "config.main", TaskID: "web/config/0", Operation: "overwrite-file"}
	p.apply(event)
	event.Kind, event.Persistence = plan.EventTaskSucceeded, plan.PersistenceFailed
	p.apply(event)
	p.apply(plan.Event{Kind: plan.EventTaskBlocked, Host: "host", TaskID: "web/config/1", Error: "save progress failed"})
	p.apply(plan.Event{Kind: plan.EventApplyCanceled})
	p.stop()
	text := out.String()
	for _, want := range []string{"service=web component=config.main", "Operation completed; progress save failed:", "save progress failed", "Apply canceled; waiting for active tasks"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
}

func TestCLIProgressFlagsAndCancellation(t *testing.T) {
	inventory, _ := testInventory(t)
	for _, mode := range []string{"auto", "plain", "off"} {
		code, stdout, stderr := run(t, "diff", "-i", inventory, "--progress="+mode)
		if code != 0 || !strings.Contains(stdout, "overwrite-file") {
			t.Fatalf("mode %s: %d %s %s", mode, code, stdout, stderr)
		}
		if mode == "off" && stderr != "" {
			t.Fatalf("off progress: %s", stderr)
		}
		if mode != "off" && !strings.Contains(stderr, "Preparing:") {
			t.Fatalf("missing %s progress: %s", mode, stderr)
		}
		if strings.Contains(stdout, "Preparing:") {
			t.Fatal("progress polluted plan output")
		}
	}
	if code, _, _ := run(t, "diff", "--progress=invalid"); code != 2 {
		t.Fatal("invalid mode accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := RunContext(ctx, []string{"apply", "-y", "--progress=off", "-i", inventory}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("cancellation lost: %d %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Result:") {
		t.Fatal("canceled preparation reached apply")
	}
}

func TestCanceledConfirmationLeavesInputWithCaller(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := readConfirmation(ctx, reader); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation did not cancel")
	}
	if _, err := io.WriteString(writer, "yes\n"); err != nil {
		t.Fatal(err)
	}
	next := make([]byte, 4)
	if _, err := io.ReadFull(reader, next); err != nil || string(next) != "yes\n" {
		t.Fatalf("canceled confirmation consumed input: %q %v", next, err)
	}
}
