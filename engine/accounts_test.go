package engine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/subuk/richt/accounts"
	"github.com/subuk/richt/config"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

type accountOrderedExecutor struct{ orderedExecutor }

func (e *accountOrderedExecutor) ForHost(plan.Host) (plan.Executor, error) { return e, nil }

type missingAccount int

func (missingAccount) Error() string { return "account not found" }
func (missingAccount) ExitCode() int { return 2 }

// This observation-only target proves that preparation never creates accounts
// or paths. Backend execution and target shell behavior have their own tests.
func accountTarget(t *testing.T, exists bool) config.Target {
	t.Helper()
	return config.Target{RunInput: func(_ context.Context, input []byte, command string, args ...string) ([]byte, error) {
		if command != "python3" {
			t.Fatalf("unexpected helper %s", command)
		}
		var request struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(input, &request); err != nil || request.Action != "observe" {
			t.Fatal("preparation mutated files")
		}
		if !exists {
			return fileObservation("absent", ""), nil
		}
		return fileObservation("regular", "frozen"), nil
	}, Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
		switch command {
		case "uname":
			return []byte("Linux\n"), nil
		case "getent":
			db := args[0]
			if db == "-s" {
				db = args[2]
			}
			if !exists {
				if args[0] == "-s" {
					return nil, nil
				}
				return nil, missingAccount(2)
			}
			if db == "passwd" {
				return []byte("app:x:417:418::/nonexistent:/usr/sbin/nologin\n"), nil
			}
			if db == "group" {
				return []byte("app:x:418:\n"), nil
			}
		case "unshare":
			if args[len(args)-1] != "richt-account-probe" {
				t.Fatalf("preparation mutated account: %s %v", command, args)
			}
			return nil, nil
		case "sh":
			if len(args) >= 3 && args[2] == "richt-account-check" {
				return nil, nil
			}
			if len(args) < 8 {
				t.Fatalf("unexpected target script: %v", args)
			}
			// The projected name bypass applies only during observation. A new
			// automatic identity must not cause an early getent lookup.
			if args[4] != "" || args[5] != "" {
				t.Fatalf("looked up a managed identity before its account task: %v", args[3:])
			}
			if strings.Contains(args[1], "stat -Lc '%d") || strings.Contains(args[1], "stat -c '%d") {
				if !exists {
					return []byte("missing\n"), nil
				}
				return []byte("present\n42\n43\n640\n417\n418\n-\n-\n"), nil
			}
			if strings.Contains(args[1], "printf '%s\\n%s\\n'") {
				return []byte("-\n-\n"), nil
			}
			if strings.Contains(args[1], "stat -Lc '%a") {
				if !exists {
					return []byte("missing\n0\n0\n0\n-\n-\n\n"), nil
				}
				return []byte("present\n640\n417\n418\n-\n-\n\nfrozen"), nil
			}
			if strings.Contains(args[1], "printf '%s") {
				return []byte("-\n-\n"), nil
			}
		}
		t.Fatalf("unexpected preparation command %s %v", command, args)
		return nil, errors.New("unexpected command")
	}}
}

func accountService(root string) inventory.Service {
	return inventory.Service{Configs: []inventory.Config{
		{Name: "group", Type: "group", Group: "app"},
		{Name: "user", Type: "user", User: "app", Group: "app"},
		{Name: "settings", Path: filepath.Join(root, "settings"), Owner: "app", Group: "app", Template: inventory.Template{Inline: inline("frozen")}},
		{Type: "file", Path: filepath.Join(root, "plain"), Owner: "app", Group: "app"},
		{Type: "directory", Path: filepath.Join(root, "directory"), Owner: "app", Group: "app"},
	}, Processes: []inventory.Process{{Type: "oneshot", Command: []string{"/bin/notify"}, RunOn: []string{"config.user"}}}}
}

func TestAccountsPrepareBeforeFilesAcrossTransportsAndContinueSavedPlan(t *testing.T) {
	for _, connection := range []inventory.Transport{{Type: "local"}, {Type: "local", Sudo: true}, {Type: "ssh", Host: "target", User: "operator", Sudo: true}} {
		t.Run(connection.Type+map[bool]string{true: "-sudo"}[connection.Sudo], func(t *testing.T) {
			target := accountTarget(t, false)
			connected := false
			e := Local{Accounts: accounts.Backend{Runner: target.Run}, Files: &target, Connect: func(c inventory.Transport) (config.Target, error) {
				connected = true
				if c != connection {
					t.Fatal(c, connection)
				}
				return target, nil
			}}
			r := resolved(accountService(tempDir(t)))
			r.Hosts[0].Transport = connection
			p, err := e.Diff(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if connected != (connection.Type == "ssh" || connection.Sudo) {
				t.Fatal("account backend used a different transport")
			}
			expected := []string{"app/config/0", "app/config/1", "app/config/2", "app/config/3", "app/config/4", "app/process/0"}
			if !reflect.DeepEqual(taskIDs(p), expected) {
				t.Fatal(taskIDs(p))
			}
			if p.Hosts[0].Tasks[0].Account.GID != nil || p.Hosts[0].Tasks[1].Account.UID != nil {
				t.Fatal("automatic IDs were frozen")
			}
			if f := p.Hosts[0].Tasks[2].File; f.Owner != "app" || f.Group != "app" || !f.Write {
				t.Fatal(f)
			}
			if got := p.Hosts[0].Tasks[5].Oneshot.Triggers; !reflect.DeepEqual(got, []string{"config.user"}) {
				t.Fatal(got)
			}
			filename := filepath.Join(tempDir(t), "accounts.json")
			if err := plan.Save(filename, p); err != nil {
				t.Fatal(err)
			}
			store, err := plan.Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			executor := &accountOrderedExecutor{orderedExecutor{fail: "app/config/3"}}
			if _, err := plan.Apply(context.Background(), store.Plan, executor, store.Save); err == nil {
				t.Fatal("expected failure")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			// The inventory no longer supplies the frozen account or file inputs.
			r.Hosts[0].Services = nil
			store, err = plan.Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			executor = &accountOrderedExecutor{}
			if _, err := plan.Apply(context.Background(), store.Plan, executor, store.Save); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(executor.executed, expected[3:]) {
				t.Fatal("continuation repeated account creation", executor.executed)
			}
		})
	}
}

func TestUnchangedAccountsAndFileAttributesScheduleNothing(t *testing.T) {
	target := accountTarget(t, true)
	e := Local{Accounts: accounts.Backend{Runner: target.Run}, Files: &target}
	p, err := e.Diff(context.Background(), resolved(accountService(tempDir(t))))
	if err != nil || len(p.Hosts[0].Tasks) != 0 {
		t.Fatal(p, err)
	}
}

func TestAccountRemovalReversesConsumersAndAccounts(t *testing.T) {
	target := accountTarget(t, true)
	e := Local{Accounts: accounts.Backend{Runner: target.Run}, Files: &target}
	r := resolved(accountService(tempDir(t)))
	r.Hosts[0].Services[0].State = "absent"
	p, err := e.Diff(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"app/config/4", "app/config/3", "app/config/2", "app/config/1", "app/config/0"}
	if !reflect.DeepEqual(taskIDs(p), expected) {
		t.Fatal(taskIDs(p))
	}
	target = accountTarget(t, false)
	e.Accounts.Runner, e.Files = target.Run, &target
	p, err = e.Diff(context.Background(), r)
	if err != nil || len(p.Hosts[0].Tasks) != 0 {
		t.Fatal(p, err)
	}
}

func TestLaterDeclaredAccountsFailEvenWhenTheyAlreadyExist(t *testing.T) {
	for _, exists := range []bool{false, true} {
		for _, consumer := range []inventory.Config{
			{Type: "user", User: "app", Group: "app"},
			{Type: "user", User: "app", SupplementaryGroups: &[]string{"app"}},
			{Path: "/tmp/settings", Group: "app", Template: inventory.Template{Inline: inline("frozen")}},
			{Type: "directory", Path: "/tmp/data", Group: "app"},
		} {
			target := accountTarget(t, exists)
			e := Local{Accounts: accounts.Backend{Runner: target.Run}, Files: &target}
			s := inventory.Service{Configs: []inventory.Config{consumer, {Type: "group", Group: "app"}}}
			if _, err := e.Diff(context.Background(), resolved(s)); err == nil || !strings.Contains(err.Error(), "declare the account before its consumers") {
				t.Fatal(consumer, exists, err)
			}
		}
	}
}

func TestAccountDependenciesComposeAcrossServices(t *testing.T) {
	target := accountTarget(t, false)
	e := Local{Accounts: accounts.Backend{Runner: target.Run}, Files: &target}
	s := accountService(tempDir(t))
	r := resolved(inventory.Service{Configs: s.Configs[:2]})
	r.Hosts[0].Services = append(r.Hosts[0].Services, inventory.ResolvedService{Assignment: inventory.Assignment{Name: "consumer", State: "present"}, Definition: inventory.Service{Configs: s.Configs[2:]}})
	if p, err := e.Diff(context.Background(), r); err != nil || len(p.Hosts[0].Tasks) != 5 {
		t.Fatal(p, err)
	}
	r.Hosts[0].Services[0], r.Hosts[0].Services[1] = r.Hosts[0].Services[1], r.Hosts[0].Services[0]
	if _, err := e.Diff(context.Background(), r); err == nil || !strings.Contains(err.Error(), "precedes user") {
		t.Fatal(err)
	}
}

func TestPresentConsumersRejectDeclaredAccountRemovalInEitherServiceOrder(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		consumer   inventory.Config
	}{
		{"template-owner", "user", inventory.Config{Path: "/tmp/settings", Owner: "app", Template: inventory.Template{Inline: inline("frozen")}}},
		{"template-group", "group", inventory.Config{Path: "/tmp/settings", Group: "app", Template: inventory.Template{Inline: inline("frozen")}}},
		{"file-owner", "user", inventory.Config{Type: "file", Path: "/tmp/plain", Owner: "app"}},
		{"directory-group", "group", inventory.Config{Type: "directory", Path: "/tmp/data", Group: "app"}},
		{"primary-group", "group", inventory.Config{Type: "user", User: "worker", Group: "app"}},
		{"supplementary-group", "group", inventory.Config{Type: "user", User: "worker", SupplementaryGroups: &[]string{"app"}}},
	} {
		for _, removeFirst := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "-remove-first", false: "-remove-later"}[removeFirst], func(t *testing.T) {
				target := accountTarget(t, true)
				e := Local{Accounts: accounts.Backend{Runner: target.Run}, Files: &target}
				r := resolved(inventory.Service{Configs: []inventory.Config{tc.consumer}})
				account := inventory.Config{Type: tc.kind, User: "app"}
				if tc.kind == "group" {
					account.User, account.Group = "", "app"
				}
				r.Hosts[0].Services = append(r.Hosts[0].Services, inventory.ResolvedService{
					Assignment: inventory.Assignment{Name: "remove", State: "absent"},
					Definition: inventory.Service{Configs: []inventory.Config{account}},
				})
				if removeFirst {
					r.Hosts[0].Services[0], r.Hosts[0].Services[1] = r.Hosts[0].Services[1], r.Hosts[0].Services[0]
				}
				if _, err := e.Diff(context.Background(), r); err == nil || !strings.Contains(err.Error(), "that account is declared absent") {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestAccountRemovalDoesNotAddUnspecifiedOwnershipDependencies(t *testing.T) {
	for _, removeFirst := range []bool{false, true} {
		services := []preparedService{
			{service: inventory.ResolvedService{Assignment: inventory.Assignment{Name: "consumer", State: "present"}}, files: []preparedFile{{file: &plan.FileChange{Path: "/tmp/unmanaged-owner"}}}},
			{service: inventory.ResolvedService{Assignment: inventory.Assignment{Name: "remove", State: "absent"}}, files: []preparedFile{{definition: &inventory.Config{Type: "user", User: "app"}}, {definition: &inventory.Config{Type: "group", Group: "app"}}}},
		}
		if removeFirst {
			services[0], services[1] = services[1], services[0]
		}
		if err := checkAccountOrder(services); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnedSystemdFilesFollowAccountCreationAndPrecedeRemoval(t *testing.T) {
	for _, absent := range []bool{false, true} {
		target := accountTarget(t, absent)
		e := Local{Accounts: accounts.Backend{Runner: target.Run}, Files: &target, Systemd: fakeSystemd("inactive", true)}
		s := accountService(tempDir(t))
		s.Processes = []inventory.Process{{Unit: "app.service", UnitFile: &inventory.Config{
			Path: "/etc/systemd/system/app.service", Owner: "app", Group: "app", Template: inventory.Template{Inline: inline("[Service]\nExecStart=/bin/true\n")},
		}}}
		r := resolved(s)
		if absent {
			r.Hosts[0].Services[0].State = "absent"
		}
		p, err := e.Diff(context.Background(), r)
		if err != nil {
			t.Fatal(absent, err)
		}
		ids := taskIDs(p)
		if absent {
			want := []string{"app/process/0/unit", "app/config/4", "app/config/3", "app/config/2", "app/config/1", "app/config/0", "app/systemd/daemon-reload"}
			if !reflect.DeepEqual(ids, want) {
				t.Fatal(ids)
			}
		} else if len(ids) < 6 || ids[0] != "app/config/0" || ids[1] != "app/config/1" || ids[5] != "app/process/0/unit" {
			t.Fatal(ids)
		}
	}
}

// File helper protocol fixtures contain the same bytes used for the diff.
func fileObservation(kind, content string) []byte {
	state := &plan.FileState{Kind: kind}
	if kind == "regular" {
		sum := sha256.Sum256([]byte(content))
		state.SHA256 = hex.EncodeToString(sum[:])
		state.Metadata = &plan.FileMetadata{UID: 417, GID: 418, Mode: 0640, Xattrs: map[string]string{}}
	}
	out, _ := json.Marshal(map[string]any{"state": state, "content": base64.StdEncoding.EncodeToString([]byte(content))})
	return out
}
