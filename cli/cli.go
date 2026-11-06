// Package cli handles richt command-line arguments and output.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/subuk/richt/engine"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/inventoryyaml"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/source"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Version is set with -ldflags when building a release.
var Version = "dev"

type arguments struct {
	Version   kong.VersionFlag `help:"Print version information and exit."`
	Inventory struct {
		Validate selection `cmd:"" help:"Validate inventory and selected assignments."`
		Show     selection `cmd:"" help:"Show resolved inventory as JSON."`
	} `cmd:"" help:"Inspect YAML inventory."`
	Diff struct {
		selection
		Output   string `short:"o" help:"Save an executable JSON plan." type:"path"`
		Progress string `default:"auto" enum:"auto,plain,off" help:"Progress on stderr: auto, plain, or off."`
	} `cmd:"" help:"Show changes without modifying resources."`
	Apply struct {
		selection
		Plan        string `arg:"" optional:"" help:"Saved plan to execute and update." type:"path"`
		Yes         bool   `short:"y" help:"Apply without a confirmation prompt."`
		Parallelism int    `default:"20" help:"Maximum number of hosts to apply concurrently (positive)."`
		Progress    string `default:"auto" enum:"auto,plain,off" help:"Progress on stderr: auto, plain, or off."`
	} `cmd:"" help:"Apply a saved plan, or build and apply changes."`
}
type selection struct {
	Inventory       string   `short:"i" help:"YAML inventory path or HTTPS URL."`
	SourceConfig    string   `help:"Explicit local YAML settings for HTTPS authentication, CA and secret bindings." type:"path"`
	InventorySHA256 string   `name:"inventory-sha256" help:"Expected SHA-256 of an HTTPS inventory."`
	Hosts           []string `name:"host" help:"Select host names (repeat or comma separate)."`
	Tags            []string `name:"tag" help:"Select hosts carrying these tags."`
	Services        []string `name:"service" help:"Select assigned service names."`
}

func (s selection) load(ctx context.Context) (*inventory.Resolved, *source.Client, error) {
	path := s.Inventory
	if path == "" {
		path = "inventory.yaml"
	}
	// Keep Kong's established local path expansion without treating a URL as
	// a filesystem path (which would expand URL contents and make it absolute).
	if !source.IsURL(path) {
		path = kong.ExpandPath(path)
	}
	sources, bindings, err := loadSourceConfig(s.SourceConfig)
	if err != nil {
		return nil, nil, err
	}
	i, err := inventoryyaml.LoadWithOptions(ctx, path, inventoryyaml.LoadOptions{Sources: sources, SHA256: s.InventorySHA256, SecretSources: bindings})
	if err != nil {
		return nil, nil, err
	}
	r, err := i.Resolve(inventory.Selectors{Hosts: s.Hosts, Tags: s.Tags, Services: s.Services})
	return r, sources, err
}
func (s selection) specified() bool {
	return s.Inventory != "" || s.SourceConfig != "" || s.InventorySHA256 != "" || len(s.Hosts) > 0 || len(s.Tags) > 0 || len(s.Services) > 0
}

// Run executes richt and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdout, stderr)
}

// RunContext executes richt with cancellation shared by preparation and apply.
func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var flags arguments
	var exited bool
	var exitCode int
	parser, err := kong.New(&flags,
		kong.Name("richt"),
		kong.Description("Manage host configuration with an explicit plan: RPM and DEB packages, downloaded files, tar.gz archives, configs, and processes."),
		kong.Vars{"version": "richt " + Version},
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { exited, exitCode = true, code }),
	)
	if err != nil {
		fmt.Fprintf(stderr, "richt: %v\n", err)
		return 1
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	parsed, err := parser.Parse(args)
	if exited {
		return exitCode
	}
	if err != nil {
		fmt.Fprintf(stderr, "richt: %v\n", err)
		return 2
	}
	if err = executeContext(ctx, parsed.Command(), flags, os.Stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "richt: %v\n", err)
		return 1
	}
	return 0
}

func execute(command string, f arguments, stdin *os.File, out io.Writer) error {
	return executeContext(context.Background(), command, f, stdin, out, io.Discard)
}

func executeContext(ctx context.Context, command string, f arguments, stdin *os.File, out, stderr io.Writer) error {
	switch command {
	case "inventory validate":
		r, _, err := f.Inventory.Validate.load(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Inventory valid: %d host(s).\n", len(r.Hosts))
		return err
	case "inventory show":
		r, _, err := f.Inventory.Show.load(ctx)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "diff":
		r, sources, err := f.Diff.selection.load(ctx)
		if err != nil {
			return err
		}
		p, err := prepare(ctx, r, sources, stderr, f.Diff.Progress)
		if err != nil {
			return err
		}
		if err = plan.Display(out, p, engine.Local{}); err != nil {
			return err
		}
		if f.Diff.Output != "" {
			if err = plan.Save(f.Diff.Output, p); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "Saved plan: %s\n", f.Diff.Output)
		}
		return err
	case "apply", "apply <plan>":
		if f.Apply.Parallelism <= 0 {
			return fmt.Errorf("parallelism must be positive")
		}
		var p *plan.Plan
		var persist func(*plan.Plan) error
		if f.Apply.Plan != "" {
			if f.Apply.selection.specified() {
				return fmt.Errorf("inventory, source settings, and host/tag/service filters cannot be used with a saved plan")
			}
			s, err := plan.Open(f.Apply.Plan)
			if err != nil {
				return err
			}
			defer s.Close()
			p = s.Plan
			persist = s.Save
		} else {
			r, sources, err := f.Apply.selection.load(ctx)
			if err != nil {
				return err
			}
			p, err = prepare(ctx, r, sources, stderr, f.Apply.Progress)
			if err != nil {
				return err
			}
		}
		if err := plan.Display(out, p, engine.Local{}); err != nil {
			return err
		}
		if err := confirmContext(ctx, stdin, out, f.Apply.Yes); err != nil {
			return err
		}
		progress := newProgress(stderr, f.Apply.Progress, "Applying")
		result, err := plan.Apply(ctx, p, engine.Local{}, persist, plan.ApplyOptions{HostConcurrency: f.Apply.Parallelism, Observer: progress.apply})
		progressErr := progress.stop()
		result.PlanPath = f.Apply.Plan
		printErr := plan.DisplayResult(out, result)
		if err != nil {
			return err
		}
		if progressErr != nil {
			return progressErr
		}
		return printErr
	}
	return fmt.Errorf("unsupported command %q", command)
}

func prepare(ctx context.Context, r *inventory.Resolved, sources *source.Client, stderr io.Writer, mode string) (*plan.Plan, error) {
	progress := newProgress(stderr, mode, "Preparing")
	p, err := (engine.Local{}).Diff(ctx, r, engine.DiffOptions{Observer: progress.prepare, Sources: sources})
	progressErr := progress.stop()
	if err != nil {
		return nil, err
	}
	return p, progressErr
}

func confirm(stdin *os.File, out io.Writer, yes bool) error {
	return confirmContext(context.Background(), stdin, out, yes)
}

func confirmContext(ctx context.Context, stdin *os.File, out io.Writer, yes bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if yes {
		return nil
	}
	if !term.IsTerminal(int(stdin.Fd())) {
		return fmt.Errorf("confirmation requires a terminal; use -y to apply without a prompt")
	}
	if _, err := fmt.Fprint(out, "Apply these changes? [y/N] "); err != nil {
		return err
	}
	answer, err := readConfirmation(ctx, stdin)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("apply canceled: confirmation was not received")
	}
	if strings.ToLower(strings.TrimSpace(answer)) != "y" && strings.ToLower(strings.TrimSpace(answer)) != "yes" {
		return fmt.Errorf("apply canceled")
	}
	return nil
}

// Polling keeps cancellation bounded without leaving a goroutine reading from
// the caller's terminal after RunContext returns. Canonical terminal input is
// read only after it becomes available; stdin remains owned by the caller.
func readConfirmation(ctx context.Context, stdin *os.File) (string, error) {
	var answer strings.Builder
	var next [1]byte
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fds := []unix.PollFd{{Fd: int32(stdin.Fd()), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return "", err
		}
		if fds[0].Revents == 0 {
			continue
		}
		n, err := stdin.Read(next[:])
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", io.EOF
		}
		answer.WriteByte(next[0])
		if next[0] == '\n' {
			return answer.String(), nil
		}
	}
}
