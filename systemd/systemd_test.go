package systemd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/subuk/richt/plan"
)

func TestLifecycleBranches(t *testing.T) {
	for _, tc := range []struct{ active, target, reaction, want string }{{"inactive", "running", "restart", "start"}, {"inactive", "running", "reload", "start"}, {"active", "running", "reload", "reload"}, {"active", "running", "restart", "restart"}, {"active", "running", "", ""}, {"active", "stopped", "restart", "stop"}, {"inactive", "stopped", "restart", ""}} {
		t.Run(strings.Join([]string{tc.active, tc.target, tc.reaction}, "/"), func(t *testing.T) {
			active := tc.active
			var commands []string
			b := Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "show" {
					return []byte(fmt.Sprintf("LoadState=loaded\nActiveState=%s\nUnitFileState=disabled\nCanReload=yes\n", active)), nil
				}
				commands = append(commands, args[0])
				if args[0] == "stop" {
					active = "inactive"
				} else {
					active = "active"
				}
				return nil, nil
			}}
			err := b.Execute(context.Background(), plan.Action{Kind: "systemd", Process: &plan.ProcessChange{Unit: "demo.service", State: tc.target, Reaction: tc.reaction, Timeout: "1s"}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(commands, ",") != tc.want {
				t.Fatal(commands)
			}
		})
	}
}
func TestTimeoutAndCommandFailureHaveNoRetries(t *testing.T) {
	for _, commandFails := range []bool{false, true} {
		t.Run(fmt.Sprint(commandFails), func(t *testing.T) {
			calls := 0
			b := Backend{Run: func(ctx context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "show" {
					return []byte("LoadState=loaded\nActiveState=inactive\nUnitFileState=disabled\nCanReload=no\n"), nil
				}
				calls++
				if commandFails {
					return nil, errors.New("command rejected")
				}
				return nil, nil
			}}
			start := time.Now()
			err := b.Execute(context.Background(), plan.Action{Kind: "systemd", Process: &plan.ProcessChange{Unit: "demo.service", State: "running", Timeout: "30ms"}})
			if err == nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if time.Since(start) > time.Second {
				t.Fatal("timeout not bounded")
			}
		})
	}
}
func TestEnablementIsIndependent(t *testing.T) {
	state := "active"
	enabled := "enabled"
	var commands []string
	no := false
	b := Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "show" {
			return []byte("LoadState=loaded\nActiveState=" + state + "\nUnitFileState=" + enabled + "\nCanReload=no\n"), nil
		}
		commands = append(commands, args[0])
		enabled = "disabled"
		return nil, nil
	}}
	if err := b.Execute(context.Background(), plan.Action{Kind: "systemd-enable", Process: &plan.ProcessChange{Unit: "demo.service", State: "running", Enabled: &no, Timeout: "1s"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(commands, ",") != "disable" || state != "active" {
		t.Fatal(commands, state)
	}
}
func TestReloadParserResetsAndContinuations(t *testing.T) {
	got, err := ReloadSetting("[Service]\nExecReload=\nExecReload=/bin/kill \\\n -HUP $MAINPID\n", false)
	if err != nil || !got {
		t.Fatal(got, err)
	}
	got, err = ReloadSetting("[Service]\nExecReload=\n", true)
	if err != nil || got {
		t.Fatal(got, err)
	}
}
func TestUnexpectedStateDoesNotRunLifecycle(t *testing.T) {
	b := Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] != "show" {
			t.Fatal("unexpected mutation")
		}
		return []byte("LoadState=loaded\nActiveState=activating\n"), nil
	}}
	if err := b.Execute(context.Background(), plan.Action{Kind: "systemd", Process: &plan.ProcessChange{Unit: "demo.service", State: "running", Timeout: "1s"}}); err == nil {
		t.Fatal("accepted transitional state")
	}
}

func TestFinalDropInPrecedenceAndNotifyReload(t *testing.T) {
	files := []*plan.FileChange{
		{Path: "/unused/richt-order.service.d/20-reload.conf", Content: "[Service]\nExecReload=/bin/true\n"},
		{Path: "/unused/richt-order.service.d/10-reset.conf", Content: "[Service]\nExecReload=\n"},
	}
	got, err := ReloadSupported("richt-order.service", Status{}, files)
	if err != nil || !got {
		t.Fatal("component order overrode drop-in filename order", got, err)
	}
	files = append(files, &plan.FileChange{Path: "/unused/richt-order.service.d/30-reset.conf", Content: "[Service]\nExecReload=\n"})
	got, err = ReloadSupported("richt-order.service", Status{}, files)
	if err != nil || got {
		t.Fatal("later reset was ignored", got, err)
	}
	got, err = ReloadSupported("richt-order.service", Status{}, []*plan.FileChange{{Path: "/unused/richt-order.service", Content: "[Service]\nType=notify-reload\n"}})
	if err != nil || !got {
		t.Fatal("notify-reload capability missing", got, err)
	}
}

func TestReloadSupportedReadsTargetFiles(t *testing.T) {
	var reads, globs []string
	b := Backend{
		ReadFile: func(_ context.Context, path string) ([]byte, error) {
			reads = append(reads, path)
			switch path {
			case "/target/unit.service":
				return []byte("[Service]\nExecReload=/bin/true\n"), nil
			case "/target/10-reset.conf":
				return []byte("[Service]\nExecReload=\n"), nil
			default:
				return nil, fmt.Errorf("unexpected target read %s", path)
			}
		},
		Glob: func(_ context.Context, pattern string) ([]string, error) {
			globs = append(globs, pattern)
			return nil, nil
		},
	}
	yes, err := b.ReloadSupported(context.Background(), "unit.service", Status{Fragment: "/target/unit.service", DropIns: []string{"/target/10-reset.conf"}}, nil)
	if err != nil || yes || len(reads) != 2 || len(globs) != 3 {
		t.Fatal(yes, err, reads, globs)
	}
	yes, err = b.ReloadSupported(context.Background(), "unit.service", Status{Fragment: "/target/unit.service", DropIns: []string{"/target/10-reset.conf"}}, []*plan.FileChange{{Path: "/etc/systemd/system/unit.service.d/20-reload.conf", Content: "[Service]\nExecReload=/bin/true\n"}})
	if err != nil || !yes {
		t.Fatal(yes, err)
	}
}
