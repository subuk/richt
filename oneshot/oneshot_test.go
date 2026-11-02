package oneshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/subuk/richt/plan"
)

func change(command ...string) *plan.OneshotChange {
	return &plan.OneshotChange{Command: command, Timeout: "5s", Triggers: []string{"config.main"}}
}

func TestResolveShowsPreparedArgvReasonAndSuccessWithoutExecution(t *testing.T) {
	calls := 0
	b := Backend{Run: func(context.Context, string, ...string) error { calls++; return nil }}
	c := change("/bin/tool", "literal argument", "$(touch bad)", "")
	actions, err := b.Resolve(plan.Task{Operation: "oneshot", Oneshot: c})
	if err != nil || calls != 0 || len(actions) != 1 || actions[0].Oneshot != c {
		t.Fatal(actions, err, calls)
	}
	display := strings.Join(actions[0].Description, "\n")
	for _, text := range []string{`"/bin/tool", "literal argument", "$(touch bad)", ""`, "config.main", "exit status 0", "5s", "discarded"} {
		if !strings.Contains(display, text) {
			t.Fatal("missing display detail", text, display)
		}
	}
}

func TestExecuteRunsLiteralArgvAndDiscardsOutput(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "args")
	script := filepath.Join(dir, "tool")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$ONESHOT_ARGS\"\nprintf 'private output'\nprintf 'private error' >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONESHOT_ARGS", output)
	args := []string{"$(touch bad)", "a'b", "", "line\nvalue"}
	c := change(append([]string{script}, args...)...)
	if err := (Backend{}).Execute(context.Background(), plan.Action{Kind: "oneshot", Oneshot: c}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(data, []byte(strings.Join(args, "\x00")+"\x00")) {
		t.Fatal(string(data), err)
	}
}

func TestFailureTimeoutAndRunnerErrorsHideAllOutput(t *testing.T) {
	for _, command := range [][]string{
		{"sh", "-c", "printf ONESHOT_PRIVATE_CANARY; printf ONESHOT_PRIVATE_CANARY >&2; exit 27"},
		{"ONESHOT_PRIVATE_CANARY_NONEXISTENT"},
	} {
		err := (Backend{}).Execute(context.Background(), plan.Action{Kind: "oneshot", Oneshot: change(command...)})
		if err == nil || strings.Contains(err.Error(), "PRIVATE_CANARY") {
			t.Fatal("unsafe command error", err)
		}
		if command[0] == "sh" && !strings.Contains(err.Error(), "exit status 27") {
			t.Fatal(err)
		}
	}
	b := Backend{Run: func(context.Context, string, ...string) error { return errors.New("ONESHOT_PRIVATE_CANARY") }}
	if err := b.Execute(context.Background(), plan.Action{Kind: "oneshot", Oneshot: change("tool")}); err == nil || strings.Contains(err.Error(), "PRIVATE_CANARY") {
		t.Fatal(err)
	}
	c := change("sh", "-c", "exec sleep 30")
	c.Timeout = "20ms"
	start := time.Now()
	err := (Backend{}).Execute(context.Background(), plan.Action{Kind: "oneshot", Oneshot: c})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatal(err, time.Since(start))
	}
}

func TestResolveAndExecuteRejectMalformedPayloadsBeforeRunning(t *testing.T) {
	for _, c := range []*plan.OneshotChange{
		nil, change(), change(""), change(" "), change("bad\x00arg"),
		{Command: []string{"tool"}, Timeout: "0s", Triggers: []string{"config.main"}},
		{Command: []string{"tool"}, Timeout: "5s"},
		{Command: []string{"tool"}, Timeout: "5s", Triggers: []string{"process.main"}},
		{Command: []string{"tool"}, Timeout: "5s", Triggers: []string{"config.main", "config.main"}},
	} {
		calls := 0
		b := Backend{Run: func(context.Context, string, ...string) error { calls++; return nil }}
		if _, err := b.Resolve(plan.Task{Operation: "oneshot", Oneshot: c}); err == nil {
			t.Fatal("accepted invalid change", c)
		}
		if err := b.Execute(context.Background(), plan.Action{Kind: "oneshot", Oneshot: c}); err == nil || calls != 0 {
			t.Fatal(err, calls)
		}
	}
	c := change("tool", "")
	got := []string{}
	b := Backend{Run: func(_ context.Context, name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}}
	if err := b.Execute(context.Background(), plan.Action{Kind: "oneshot", Oneshot: c}); err != nil || !reflect.DeepEqual(got, c.Command) {
		t.Fatal(got, err)
	}
}

func TestDeadlineFailsEvenWhenRunnerReturnsNilAfterCancellation(t *testing.T) {
	c := change("tool")
	c.Timeout = "1ms"
	b := Backend{Run: func(ctx context.Context, _ string, _ ...string) error { <-ctx.Done(); return nil }}
	if err := b.Execute(context.Background(), plan.Action{Kind: "oneshot", Oneshot: c}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
