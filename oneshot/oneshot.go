// Package oneshot executes prepared argv commands after linked resource changes.
package oneshot

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/transport"
)

// Backend executes commands through a boundary that discards command output.
// A nil Run uses unprivileged local transport.
type Backend struct {
	Run func(context.Context, string, ...string) error
}

func (Backend) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "oneshot" || t.Account != nil || t.Artifact != nil || t.File != nil || t.RPM != nil || t.Process != nil || t.DEB != nil {
		return nil, errors.New("invalid oneshot operation or mixed payload")
	}
	if err := t.Oneshot.Validate(); err != nil {
		return nil, err
	}
	c := t.Oneshot
	args := make([]string, len(c.Command))
	for i, arg := range c.Command {
		args[i] = strconv.Quote(arg)
	}
	return []plan.Action{{Kind: "oneshot", Oneshot: c, Description: []string{
		"run argv: [" + strings.Join(args, ", ") + "]",
		"triggered by changed inputs: " + strings.Join(c.Triggers, ", "),
		"success: exit status 0; timeout " + c.Timeout + "; stdout/stderr discarded; no retries",
	}}}, nil
}

func (b Backend) Execute(ctx context.Context, a plan.Action) error {
	if a.Kind != "oneshot" || a.File != nil || a.RPM != nil || a.Process != nil || a.DEB != nil {
		return errors.New("invalid oneshot action or mixed payload")
	}
	if err := a.Oneshot.Validate(); err != nil {
		return err
	}
	c := a.Oneshot
	timeout, _ := time.ParseDuration(c.Timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := b.Run
	if run == nil {
		client, _ := transport.New(inventory.Transport{Type: "local"})
		run = client.RunDiscard
	}
	err := run(ctx, c.Command[0], c.Command[1:]...)
	if err != nil || ctx.Err() != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("oneshot command failed: %w", ctx.Err())
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("oneshot command failed with exit status %d", exit.ExitCode())
		}
		// Injected runners and target diagnostics may contain sensitive content.
		return errors.New("oneshot command could not execute")
	}
	return nil
}
