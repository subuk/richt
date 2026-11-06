// Package cli handles richt command-line arguments and output.
package cli

import (
	"bufio"
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
		Output string `short:"o" help:"Save an executable JSON plan." type:"path"`
	} `cmd:"" help:"Show changes without modifying resources."`
	Apply struct {
		selection
		Plan        string `arg:"" optional:"" help:"Saved plan to execute and update." type:"path"`
		Yes         bool   `short:"y" help:"Apply without a confirmation prompt."`
		Parallelism int    `default:"20" help:"Maximum number of hosts to apply concurrently (positive)."`
	} `cmd:"" help:"Apply a saved plan, or build and apply changes."`
}
type selection struct {
	Inventory string   `short:"i" help:"YAML inventory path." type:"path"`
	Hosts     []string `name:"host" help:"Select host names (repeat or comma separate)."`
	Tags      []string `name:"tag" help:"Select hosts carrying these tags."`
	Services  []string `name:"service" help:"Select assigned service names."`
}

func (s selection) load() (*inventory.Resolved, error) {
	path := s.Inventory
	if path == "" {
		path = "inventory.yaml"
	}
	i, err := inventoryyaml.Load(path)
	if err != nil {
		return nil, err
	}
	return i.Resolve(inventory.Selectors{Hosts: s.Hosts, Tags: s.Tags, Services: s.Services})
}
func (s selection) specified() bool {
	return s.Inventory != "" || len(s.Hosts) > 0 || len(s.Tags) > 0 || len(s.Services) > 0
}

// Run executes richt and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
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
	if err = execute(parsed.Command(), flags, os.Stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "richt: %v\n", err)
		return 1
	}
	return 0
}

func execute(command string, f arguments, stdin *os.File, out io.Writer) error {
	switch command {
	case "inventory validate":
		r, err := f.Inventory.Validate.load()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Inventory valid: %d host(s).\n", len(r.Hosts))
		return err
	case "inventory show":
		r, err := f.Inventory.Show.load()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "diff":
		r, err := f.Diff.selection.load()
		if err != nil {
			return err
		}
		p, err := engine.Diff(r)
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
				return fmt.Errorf("inventory and host/tag/service filters cannot be used with a saved plan")
			}
			s, err := plan.Open(f.Apply.Plan)
			if err != nil {
				return err
			}
			defer s.Close()
			p = s.Plan
			persist = s.Save
		} else {
			r, err := f.Apply.selection.load()
			if err != nil {
				return err
			}
			p, err = engine.Diff(r)
			if err != nil {
				return err
			}
		}
		if err := plan.Display(out, p, engine.Local{}); err != nil {
			return err
		}
		if err := confirm(stdin, out, f.Apply.Yes); err != nil {
			return err
		}
		result, err := plan.Apply(context.Background(), p, engine.Local{}, persist, plan.ApplyOptions{HostConcurrency: f.Apply.Parallelism})
		result.PlanPath = f.Apply.Plan
		printErr := plan.DisplayResult(out, result)
		if err != nil {
			return err
		}
		return printErr
	}
	return fmt.Errorf("unsupported command %q", command)
}

func confirm(stdin *os.File, out io.Writer, yes bool) error {
	if yes {
		return nil
	}
	if !term.IsTerminal(int(stdin.Fd())) {
		return fmt.Errorf("confirmation requires a terminal; use -y to apply without a prompt")
	}
	if _, err := fmt.Fprint(out, "Apply these changes? [y/N] "); err != nil {
		return err
	}
	answer, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("apply canceled: confirmation was not received")
	}
	if strings.ToLower(strings.TrimSpace(answer)) != "y" && strings.ToLower(strings.TrimSpace(answer)) != "yes" {
		return fmt.Errorf("apply canceled")
	}
	return nil
}
