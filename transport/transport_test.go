package transport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/subuk/richt/inventory"
)

func fakeCommand(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestSSHArgumentsAndInput(t *testing.T) {
	dir := fakeCommand(t, "ssh", `printf '%s\000' "$@" > "$TRANSPORT_ARGS"
cat > "$TRANSPORT_INPUT"
printf 'stdout'
printf 'stderr must not enter stdout' >&2
`)
	argumentFile, inputFile := filepath.Join(dir, "args"), filepath.Join(dir, "input")
	t.Setenv("TRANSPORT_ARGS", argumentFile)
	t.Setenv("TRANSPORT_INPUT", inputFile)
	c, err := New(inventory.Transport{Type: "ssh", Host: "alias", User: "explicit", Port: 2222, Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("sensitive\x00payload\n")
	out, err := c.RunInput(context.Background(), input, "cat", "a'b", "$(touch bad)", "")
	if err != nil || string(out) != "stdout" {
		t.Fatalf("RunInput = %q, %v", out, err)
	}
	b, _ := os.ReadFile(argumentFile)
	args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	want := []string{"-T", "-o", "BatchMode=yes", "-o", "NumberOfPasswordPrompts=0", "-o", "RequestTTY=no", "-o", "RemoteCommand=none", "-o", "SessionType=default", "-o", "StdinNull=no", "-o", "ForkAfterAuthentication=no", "-l", "explicit", "-p", "2222", "--", "alias", `'sudo' '-n' '--' 'cat' 'a'"'"'b' '$(touch bad)' ''`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("arguments = %#v; want %#v", args, want)
	}
	got, _ := os.ReadFile(inputFile)
	if !bytes.Equal(got, input) || bytes.Contains(b, input) {
		t.Fatal("input was not transmitted exclusively through stdin")
	}
}

func TestRemoteArgumentsRemainLiteral(t *testing.T) {
	fakeCommand(t, "ssh", `for last do :; done
exec sh -c "$last"
`)
	c, _ := New(inventory.Transport{Type: "ssh", Host: "alias"})
	args := []string{"plain", "a'b", "$(printf dangerous)", "\nline\n", "", "☃"}
	out, err := c.Run(context.Background(), "printf", append([]string{"%s\\000"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(strings.Join(args, "\x00") + "\x00")
	if !bytes.Equal(out, want) {
		t.Fatalf("arguments changed: %q != %q", out, want)
	}
}

func TestLocalSudoIsExplicitAndNoninteractive(t *testing.T) {
	fakeCommand(t, "sudo", `[ "$1" = -n ] && [ "$2" = -- ] || exit 90
shift 2
exec "$@"
`)
	for _, sudo := range []bool{false, true} {
		c, _ := New(inventory.Transport{Type: "local", Sudo: sudo})
		out, err := c.RunInput(context.Background(), []byte("input"), "cat")
		if err != nil || string(out) != "input" {
			t.Fatalf("sudo=%v: %q %v", sudo, out, err)
		}
	}
}

func TestErrorsHideOutputArgumentsAndStdin(t *testing.T) {
	c, _ := New(inventory.Transport{Type: "local"})
	out, err := c.RunInput(context.Background(), []byte("stdin-secret"), "sh", "-c", "cat; printf stderr-secret >&2; exit 42", "argument-secret")
	if err == nil || out != nil {
		t.Fatalf("failed command = %q %v", out, err)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 || len(exit.Stderr) != 0 {
		t.Fatalf("exit status not safely preserved: %v", err)
	}
	for _, secret := range []string{"stdin-secret", "stderr-secret", "argument-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed %q", secret)
		}
	}
}

func TestCancellation(t *testing.T) {
	fakeCommand(t, "ssh", "exec sleep 30\n")
	c, _ := New(inventory.Transport{Type: "ssh", Host: "alias"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Run(ctx, "true")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestSanitizedTransportDiagnostics(t *testing.T) {
	fakeCommand(t, "ssh", `printf '%s\n' "$TRANSPORT_STDERR" >&2
exit "$TRANSPORT_EXIT"
`)
	fakeCommand(t, "sudo", `printf '%s\n' "$TRANSPORT_STDERR" >&2
exit "$TRANSPORT_EXIT"
`)
	for _, tt := range []struct {
		name, diagnostic, want string
		connection             inventory.Transport
		code                   string
	}{
		{"authentication", "private-user@private-host: Permission denied (publickey,password).", "SSH authentication failed", inventory.Transport{Type: "ssh", Host: "alias"}, "255"},
		{"unknown host key", "No ED25519 host key is known for private-host and you have requested strict checking.\nHost key verification failed.", "SSH host key is unknown", inventory.Transport{Type: "ssh", Host: "alias"}, "255"},
		{"changed host key", "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nHost key verification failed.", "SSH host key has changed", inventory.Transport{Type: "ssh", Host: "alias"}, "255"},
		{"refused", "ssh: connect to host private-host port 22: Connection refused", "SSH connection was refused", inventory.Transport{Type: "ssh", Host: "alias"}, "255"},
		{"unreachable", "ssh: connect to host private-host port 22: No route to host", "SSH target is unreachable", inventory.Transport{Type: "ssh", Host: "alias"}, "255"},
		{"timeout", "ssh: connect to host private-host port 22: Connection timed out", "SSH connection timed out", inventory.Transport{Type: "ssh", Host: "alias"}, "255"},
		{"dns", "ssh: Could not resolve hostname private-host: Name or service not known", "SSH hostname could not be resolved", inventory.Transport{Type: "ssh", Host: "alias"}, "255"},
		{"remote sudo password", "sudo: a password is required", "sudo requires a password", inventory.Transport{Type: "ssh", Host: "alias", Sudo: true}, "1"},
		{"local sudo password", "sudo: a password is required", "sudo requires a password", inventory.Transport{Type: "local", Sudo: true}, "1"},
		{"sudo authorization", "Sorry, user private-user is not allowed to execute '/private-command' as root on private-host.", "sudo authorization was denied", inventory.Transport{Type: "local", Sudo: true}, "1"},
		{"unknown", "unknown error about private-user", "ssh command failed", inventory.Transport{Type: "ssh", Host: "alias"}, "1"},
		{"remote command", "Permission denied (private-error)", "ssh command failed", inventory.Transport{Type: "ssh", Host: "alias"}, "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TRANSPORT_STDERR", tt.diagnostic+"\nsecret-bearing-stderr\n")
			t.Setenv("TRANSPORT_EXIT", tt.code)
			c, err := New(tt.connection)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.RunInput(context.Background(), []byte("stdin-secret"), "private-command", "argument-secret")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("diagnostic = %v; want %q", err, tt.want)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || len(exit.Stderr) != 0 {
				t.Fatalf("sanitized exit status lost: %v", err)
			}
			for _, secret := range []string{"private-user", "private-host", "private-command", "secret-bearing-stderr", "stdin-secret", "argument-secret", "private-error"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("diagnostic exposed %q: %v", secret, err)
				}
			}
		})
	}
}

func TestConnectionValidation(t *testing.T) {
	for _, connection := range []inventory.Transport{{Type: "ssh"}, {Type: "ssh", Host: "-option"}, {Type: "ssh", Host: "a\nb"}, {Type: "ssh", Host: "user@host"}, {Type: "ssh", Host: "ssh://host:22"}, {Type: "ssh", Host: "host", User: "a b"}, {Type: "ssh", Host: "host", Port: -1}, {Type: "local", Host: "host"}, {Type: "unknown"}} {
		if _, err := New(connection); err == nil {
			t.Errorf("accepted %#v", connection)
		}
	}
}
