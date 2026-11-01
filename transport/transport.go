// Package transport executes target commands through the local system or the
// system OpenSSH client. It does not resolve SSH configuration into saved plans.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/subuk/richt/inventory"
)

// Client retains the explicit connection inputs. OpenSSH reads its normal
// configuration, identities, agent, and known-hosts files on every invocation.
type Client struct{ Connection inventory.Transport }

func New(connection inventory.Transport) (*Client, error) {
	if err := Validate(connection); err != nil {
		return nil, err
	}
	return &Client{Connection: connection}, nil
}

// Validate checks explicit connection inputs without contacting a host.
func Validate(c inventory.Transport) error {
	if c.Type != "local" && c.Type != "ssh" {
		return errors.New("transport type must be local or ssh")
	}
	if c.Port < 0 || c.Port > 65535 {
		return errors.New("transport port must be 1..65535 or omitted")
	}
	if c.Type == "local" {
		if c.Host != "" || c.User != "" || c.Port != 0 {
			return errors.New("local transport cannot set host, user or port")
		}
		return nil
	}
	if c.Host == "" || strings.HasPrefix(c.Host, "-") || strings.ContainsAny(c.Host, "/@") || strings.IndexFunc(c.Host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("SSH host must be a hostname, address, or SSH alias")
	}
	if strings.HasPrefix(c.User, "-") || strings.IndexFunc(c.User, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("invalid SSH user")
	}
	return nil
}

// Run returns target stdout. Stderr is never mixed with file contents or
// included in errors, because target diagnostics may contain sensitive inputs.
func (c *Client) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return c.RunInput(ctx, nil, name, args...)
}

// RunInput delivers bytes through stdin, never through the command line. Neither
// SSH authentication nor sudo may prompt, and no terminal is allocated.
func (c *Client) RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	if c == nil {
		return nil, errors.New("missing transport client")
	}
	if err := Validate(c.Connection); err != nil {
		return nil, err
	}
	if name == "" || strings.ContainsRune(name, '\x00') {
		return nil, errors.New("invalid target executable")
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return nil, errors.New("target arguments cannot contain NUL bytes")
		}
	}
	command, arguments := name, append([]string(nil), args...)
	if c.Connection.Sudo {
		command, arguments = "sudo", append([]string{"-n", "--", name}, arguments...)
	}
	if c.Connection.Type == "ssh" {
		remote := []string{command}
		remote = append(remote, arguments...)
		arguments = []string{"-T", "-o", "BatchMode=yes", "-o", "NumberOfPasswordPrompts=0", "-o", "RequestTTY=no", "-o", "RemoteCommand=none", "-o", "SessionType=default", "-o", "StdinNull=no", "-o", "ForkAfterAuthentication=no"}
		if c.Connection.User != "" {
			arguments = append(arguments, "-l", c.Connection.User)
		}
		if c.Connection.Port != 0 {
			arguments = append(arguments, "-p", strconv.Itoa(c.Connection.Port))
		}
		for i := range remote {
			remote[i] = quote(remote[i])
		}
		arguments = append(arguments, "--", c.Connection.Host, strings.Join(remote, " "))
		command = "ssh"
	}
	cmd := exec.CommandContext(ctx, command, arguments...)
	cmd.Stdin = bytes.NewReader(input)
	// Bound waiting for inherited output pipes after cancellation. Killing the
	// SSH client cannot promise cancellation of a command already accepted remotely.
	cmd.WaitDelay = time.Second
	if c.Connection.Type == "ssh" {
		cmd.Env = append(os.Environ(), "SSH_ASKPASS_REQUIRE=never")
	}
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Do not wrap ExitError directly: its Stderr field can retain secrets.
		if exit, ok := err.(*exec.ExitError); ok {
			diagnostic := c.failureDiagnostic(exit.ExitCode(), exit.Stderr)
			err = &exec.ExitError{ProcessState: exit.ProcessState}
			if diagnostic != "" {
				return nil, fmt.Errorf("%s: %w", diagnostic, err)
			}
		}
		return nil, fmt.Errorf("%s command failed: %w", command, err)
	}
	return out, nil
}

// Classify only familiar transport failures, returning fixed text. Never retain
// any diagnostic fragment: even usernames, hostnames, and command paths may be
// sensitive, and remote commands can echo their input in stderr.
func (c *Client) failureDiagnostic(code int, stderr []byte) string {
	message := strings.ToLower(string(stderr))
	if c.Connection.Type == "ssh" && code == 255 {
		switch {
		case strings.Contains(message, "remote host identification has changed"):
			return "SSH host key has changed; verify the target's known_hosts entry"
		case strings.Contains(message, "host key is known for") && strings.Contains(message, "strict checking"):
			return "SSH host key is unknown; verify it through your normal SSH setup"
		case strings.Contains(message, "host key verification failed"):
			return "SSH host key verification failed; verify the target's known_hosts entry"
		case strings.Contains(message, "permission denied ("), strings.Contains(message, "authentication failed"), strings.Contains(message, "no supported authentication methods"):
			return "SSH authentication failed; noninteractive authentication is required"
		case strings.Contains(message, "connection refused"):
			return "SSH connection was refused"
		case strings.Contains(message, "network is unreachable"), strings.Contains(message, "no route to host"):
			return "SSH target is unreachable"
		case strings.Contains(message, "connection timed out"), strings.Contains(message, "operation timed out"):
			return "SSH connection timed out"
		case strings.Contains(message, "could not resolve hostname"):
			return "SSH hostname could not be resolved"
		}
	}
	if c.Connection.Sudo {
		switch {
		case strings.Contains(message, "a password is required"), strings.Contains(message, "a terminal is required to read the password"), strings.Contains(message, "no tty present and no askpass program specified"):
			return "sudo requires a password; noninteractive sudo authorization is required"
		case strings.Contains(message, "not in the sudoers"), strings.Contains(message, "is not allowed to execute"), strings.Contains(message, "not allowed to run sudo"):
			return "sudo authorization was denied"
		case strings.Contains(message, "must have a tty to run sudo"):
			return "sudo requires a terminal; noninteractive sudo authorization is required"
		}
	}
	return ""
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
