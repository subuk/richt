package systemd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subuk/richt/plan"
)

func TestLifecycleBranches(t *testing.T) {
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
			for _, tc := range []struct{ active, target, reaction, want string }{{"inactive", "running", "restart", "start"}, {"inactive", "running", "reload", "start"}, {"active", "running", "reload", "reload"}, {"active", "running", "restart", "restart"}, {"active", "running", "", ""}, {"active", "stopped", "restart", "stop"}, {"inactive", "stopped", "restart", ""}} {
				if suffix == "socket" && tc.reaction == "reload" {
					continue
				}
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
					err := b.Execute(context.Background(), plan.Action{Kind: "systemd", Process: &plan.ProcessChange{Unit: "demo." + suffix, State: tc.target, Reaction: tc.reaction, Timeout: "1s"}})
					if err != nil {
						t.Fatal(err)
					}
					if strings.Join(commands, ",") != tc.want {
						t.Fatal(commands)
					}
				})
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
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
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
			if err := b.Execute(context.Background(), plan.Action{Kind: "systemd-enable", Process: &plan.ProcessChange{Unit: "demo." + suffix, State: "running", Enabled: &no, Timeout: "1s"}}); err != nil {
				t.Fatal(err)
			}
			if strings.Join(commands, ",") != "disable" || state != "active" {
				t.Fatal(commands, state)
			}
		})
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

func TestSocketCannotAcquireServiceReloadCapability(t *testing.T) {
	for _, content := range []string{"[Service]\nExecReload=/bin/true\n", "[Service]\nType=notify-reload\n"} {
		for _, path := range []string{"app.socket", "app.socket.d/20-reload.conf"} {
			b := Backend{
				ReadFile: func(context.Context, string) ([]byte, error) { t.Fatal("read socket reload settings"); return nil, nil },
				Glob: func(context.Context, string) ([]string, error) {
					t.Fatal("globbed socket reload settings")
					return nil, nil
				},
			}
			yes, err := b.ReloadSupported(context.Background(), "app.socket", Status{CanReload: true, Fragment: "/target/app.socket"}, []*plan.FileChange{{Path: "/etc/systemd/system/" + path, Content: content}})
			if err != nil || yes {
				t.Fatalf("socket gained reload from %s: %v, %v", path, yes, err)
			}
		}
	}
}

func TestSavedUnitValidationBeforeExecution(t *testing.T) {
	yes := true
	for _, operation := range []string{"systemd", "systemd-enable", "systemd-mask"} {
		for _, unit := range []string{"app.service", "app.socket", "app@instance.socket", "app.timer", "app.target", "app.path", "app.mount", "-app.socket", "/app.socket", "app.socket/child", "app socket", ".socket"} {
			t.Run(operation+"/"+unit, func(t *testing.T) {
				c := &plan.ProcessChange{Unit: unit, State: "stopped", Timeout: "1s"}
				if operation == "systemd-enable" {
					c.Enabled = &yes
				} else if operation == "systemd-mask" {
					c.Masked = &yes
				}
				valid := unit == "app.service" || unit == "app.socket" || unit == "app@instance.socket"
				b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("executed invalid unit")
					return nil, nil
				}}
				_, err := b.Resolve(plan.Task{Operation: operation, Process: c})
				if (err == nil) != valid {
					t.Fatalf("unit %q validation: %v", unit, err)
				}
				if !valid && b.Execute(context.Background(), plan.Action{Kind: operation, Process: c}) == nil {
					t.Fatal("executed invalid saved unit")
				}
			})
		}
	}
	c := &plan.ProcessChange{Unit: "app.socket", State: "running", Reaction: "reload", Timeout: "1s"}
	if _, err := (Backend{}).Resolve(plan.Task{Operation: "systemd", Process: c}); err == nil || !strings.Contains(err.Error(), "do not support reload") {
		t.Fatal("accepted socket reload", err)
	}
}

func TestReloadStillRequiresRuntimeCapability(t *testing.T) {
	b := Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] != "show" {
			t.Fatal("ran unsupported reload", args)
		}
		return []byte("LoadState=loaded\nActiveState=active\nCanReload=no\n"), nil
	}}
	err := b.Execute(context.Background(), plan.Action{Kind: "systemd", Process: &plan.ProcessChange{Unit: "app.service", State: "running", Reaction: "reload", Timeout: "1s"}})
	if err == nil || !strings.Contains(err.Error(), "does not support reload") {
		t.Fatal(err)
	}
}

func TestUnitDiagnosticsDoNotExposeContentOrUnexpectedState(t *testing.T) {
	const private = "EXAMPLE_PRIVATE_UNIT_VALUE"
	if _, err := ReloadSetting("[Service]\n"+private+"\n", false); err == nil || strings.Contains(err.Error(), private) {
		t.Fatal("unsafe parser diagnostic")
	}
	for _, property := range []string{"LoadState", "ActiveState"} {
		b := Backend{Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("LoadState=loaded\nActiveState=active\n" + property + "=" + private + "\n"), nil
		}}
		_, _, err := b.NeedsChange(context.Background(), &plan.ProcessChange{Unit: "demo.service", State: "running", Timeout: "1s"})
		if err == nil || strings.Contains(err.Error(), private) {
			t.Fatal("unsafe systemd state diagnostic")
		}
	}
	b := Backend{Glob: func(context.Context, string) ([]string, error) {
		return nil, errors.New(private)
	}}
	_, err := b.ReloadSupported(context.Background(), "demo.service", Status{}, []*plan.FileChange{{Path: "/etc/systemd/system/demo.service", Sensitive: true}})
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatal("unsafe sensitive capability diagnostic")
	}
}

func TestDefaultLocalCommandFailureDoesNotExposeUnitOutput(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'EXAMPLE_PRIVATE_UNIT_STDOUT\\n'\nprintf 'EXAMPLE_PRIVATE_UNIT_STDERR\\n' >&2\nprintf '%s\\n' \"$*\" >&2\nexit 42\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	// Exercise the actual default command boundary instead of a fake runner.
	err := (Backend{}).Execute(context.Background(), plan.Action{Kind: "systemd", Process: &plan.ProcessChange{Unit: "EXAMPLE_PRIVATE_ARGUMENT.service", State: "running", Timeout: "5s"}})
	if err == nil {
		t.Fatal("expected process command failure")
	}
	for _, diagnostic := range []string{fmt.Sprint(err), fmt.Sprintf("%+v", err)} {
		if strings.Contains(diagnostic, "EXAMPLE_PRIVATE") {
			t.Fatal("systemd diagnostic exposed unit input or output")
		}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 || len(exit.Stderr) != 0 {
		t.Fatal("systemd error retained output or lost exit status")
	}
}

func TestMaskCommandsAndVerification(t *testing.T) {
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
			for _, tc := range []struct {
				name, initial, want string
				target              bool
			}{
				{"mask-loaded", "disabled", "mask -- demo.service", true},
				{"mask-missing", "not-found", "mask -- demo.service", true},
				{"persist-runtime-mask", "masked-runtime", "mask -- demo.service", true},
				{"already-persistent", "masked", "", true},
				{"unmask-persistent", "masked", "unmask -- demo.service,unmask --runtime -- demo.service", false},
				{"unmask-runtime", "masked-runtime", "unmask -- demo.service,unmask --runtime -- demo.service", false},
				{"already-unmasked", "disabled", "", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					state := tc.initial
					var commands []string
					b := Backend{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
						if name != "systemctl" {
							t.Fatal(name)
						}
						if args[0] == "show" {
							load := "loaded"
							if strings.HasPrefix(state, "masked") {
								load = "masked"
							} else if state == "not-found" {
								load = "not-found"
							}
							return []byte("LoadState=" + load + "\nActiveState=inactive\nUnitFileState=" + state + "\n"), nil
						}
						commands = append(commands, strings.Join(args, " "))
						if args[0] == "mask" {
							state = "masked"
						} else if len(args) > 1 && args[1] == "--runtime" {
							state = "not-found"
						}
						return nil, nil
					}}
					c := &plan.ProcessChange{Unit: "demo." + suffix, State: "stopped", Masked: &tc.target, Timeout: "1s"}
					actions, err := b.Resolve(plan.Task{Operation: "systemd-mask", Process: c})
					if err != nil || len(actions) != 1 {
						t.Fatal(actions, err)
					}
					if err := b.Execute(context.Background(), actions[0]); err != nil {
						t.Fatal(err)
					}
					if got := strings.Join(commands, ","); got != strings.ReplaceAll(tc.want, "demo.service", "demo."+suffix) {
						t.Fatalf("commands %q; want %q", got, tc.want)
					}
				})
			}
		})
	}
}

func TestMaskFailureAndTimeoutDoNotRetry(t *testing.T) {
	for _, commandFails := range []bool{false, true} {
		t.Run(fmt.Sprint(commandFails), func(t *testing.T) {
			calls := 0
			b := Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "show" {
					return []byte("LoadState=loaded\nActiveState=inactive\nUnitFileState=disabled\n"), nil
				}
				calls++
				if commandFails {
					return nil, errors.New("mask rejected")
				}
				return nil, nil
			}}
			yes := true
			err := b.Execute(context.Background(), plan.Action{Kind: "systemd-mask", Process: &plan.ProcessChange{Unit: "demo.service", State: "stopped", Masked: &yes, Timeout: "30ms"}})
			if err == nil || calls != 1 {
				t.Fatal(err, calls)
			}
		})
	}
}

func TestMaskedUnitsSupportStopButRejectImplicitStart(t *testing.T) {
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
			for _, state := range []string{"running", "stopped"} {
				t.Run(state, func(t *testing.T) {
					active := "active"
					var commands []string
					b := Backend{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
						if args[0] == "show" {
							return []byte("LoadState=masked\nActiveState=" + active + "\nUnitFileState=masked\n"), nil
						}
						commands = append(commands, args[0])
						active = "inactive"
						return nil, nil
					}}
					err := b.Execute(context.Background(), plan.Action{Kind: "systemd", Process: &plan.ProcessChange{Unit: "demo." + suffix, State: state, Timeout: "1s"}})
					if state == "running" && (err == nil || len(commands) != 0) {
						t.Fatal(err, commands)
					}
					if state == "stopped" && (err != nil || strings.Join(commands, ",") != "stop") {
						t.Fatal(err, commands)
					}
				})
			}
		})
	}
}

func TestMaskResolveRejectsMixedAndMissingPayloads(t *testing.T) {
	yes := true
	base := plan.ProcessChange{Unit: "demo.service", State: "stopped", Masked: &yes, Timeout: "1s"}
	if _, err := (Backend{}).Resolve(plan.Task{Operation: "daemon-reload", Process: &base}); err == nil {
		t.Fatal("accepted a process payload on daemon-reload")
	}
	for _, operation := range []string{"systemd", "systemd-enable", "systemd-mask"} {
		for _, mutate := range []func(*plan.Task){
			func(t *plan.Task) { t.File = &plan.FileChange{} },
			func(t *plan.Task) { t.RPM = &plan.RPMChange{} },
			func(t *plan.Task) { t.Process = nil },
		} {
			task := plan.Task{Operation: operation, Process: &base}
			mutate(&task)
			if _, err := (Backend{}).Resolve(task); err == nil {
				t.Fatal("accepted mixed or missing payload", operation)
			}
		}
	}
	for _, mutate := range []func(*plan.ProcessChange){
		func(c *plan.ProcessChange) { c.Masked = nil },
		func(c *plan.ProcessChange) { c.Enabled = &yes },
		func(c *plan.ProcessChange) { c.Reaction = "restart" },
		func(c *plan.ProcessChange) { c.State = "running" },
	} {
		c := base
		mutate(&c)
		if _, err := (Backend{}).Resolve(plan.Task{Operation: "systemd-mask", Process: &c}); err == nil {
			t.Fatal("accepted invalid mask payload", c)
		}
	}
}
