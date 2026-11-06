// Package plan defines executable changes and their resumable progress.
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/subuk/richt/inventory"
)

const Version = "1.8"

type Plan struct {
	Version string `json:"version"`
	Hosts   []Host `json:"hosts"`
}
type Host struct {
	Name       string      `json:"name"`
	Transport  string      `json:"transport"`
	Connection *Connection `json:"connection,omitempty"`
	Tasks      []Task      `json:"tasks"`
}

// Connection preserves explicit inventory settings. SSH client configuration is
// deliberately read by the system SSH client at execution time.
type Connection struct {
	Host string `json:"host,omitempty"`
	User string `json:"user,omitempty"`
	Port int    `json:"port,omitempty"`
	Sudo bool   `json:"sudo,omitempty"`
}
type Task struct {
	ID        string          `json:"id"`
	Service   string          `json:"service"`
	Component string          `json:"component"`
	Operation string          `json:"operation"`
	File      *FileChange     `json:"file,omitempty"`
	RPM       *RPMChange      `json:"rpm,omitempty"`
	Artifact  *ArtifactChange `json:"artifact,omitempty"`
	Process   *ProcessChange  `json:"process,omitempty"`
	Oneshot   *OneshotChange  `json:"oneshot,omitempty"`
	Path      *PathChange     `json:"path,omitempty"`
	Storage   *StorageChange  `json:"storage,omitempty"`
	Mount     *MountChange    `json:"mount,omitempty"`
	Account   *AccountChange  `json:"account,omitempty"`
	Progress  Progress        `json:"progress"`
}
type FileChange struct {
	AtomicWrites bool           `json:"atomic_writes"`
	Backup       bool           `json:"backup"`
	OperationID  string         `json:"operation_id"`
	Expected     *FileState     `json:"expected"`
	Desired      *FileState     `json:"desired"`
	BackupPath   string         `json:"backup_path,omitempty"`
	Path         string         `json:"path"`
	State        string         `json:"state"`
	Content      string         `json:"content,omitempty"`
	Patch        string         `json:"patch,omitempty"`
	Sensitive    bool           `json:"sensitive,omitempty"`
	Write        bool           `json:"write,omitempty"`
	Owner        string         `json:"owner,omitempty"`
	Group        string         `json:"group,omitempty"`
	Mode         *uint32        `json:"mode,omitempty"`
	SELinux      string         `json:"selinux,omitempty"`
	Validate     *FileValidator `json:"validate,omitempty"`
}

// FileValidator retains the command and timeout reviewed with the candidate bytes.
type FileValidator struct {
	Args    []string `json:"args"`
	Timeout string   `json:"timeout"`
}
type Progress struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// RPMChange fixes a package request, while the target resolves repository dependencies.
type RPMChange struct {
	Manager         string `json:"manager"`
	Package         string `json:"package"`
	Version         string `json:"version,omitempty"`
	DisableExcludes string `json:"disable_excludes,omitempty"`
	State           string `json:"state"`
}

// UnmarshalJSON distinguishes omission from an explicitly empty or null setting.
func (c *RPMChange) UnmarshalJSON(data []byte) error {
	type plain RPMChange
	var decoded plain
	wire := struct {
		*plain
		DisableExcludes json.RawMessage `json:"disable_excludes"`
	}{plain: &decoded}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return errors.New("invalid RPM JSON")
	}
	if wire.DisableExcludes != nil {
		var value *string
		if err := json.Unmarshal(wire.DisableExcludes, &value); err != nil || value == nil || *value == "" {
			return errors.New("RPM disable_excludes must be a nonempty string when supplied")
		}
		if err := inventory.ValidateRPMDisableExcludes(*value); err != nil {
			return err
		}
		decoded.DisableExcludes = *value
	}
	*c = RPMChange(decoded)
	return nil
}

// ProcessChange contains both permitted branches; apply only selects a branch.
type ProcessChange struct {
	Unit      string `json:"unit"`
	State     string `json:"state"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Masked    *bool  `json:"masked,omitempty"`
	Timeout   string `json:"timeout"`
	Reaction  string `json:"reaction,omitempty"`
	MissingOK bool   `json:"missing_ok,omitempty"`
}

// OneshotChange fixes an argv command and the actual changed inputs that trigger it.
type OneshotChange struct {
	Command  []string `json:"command"`
	Timeout  string   `json:"timeout"`
	Triggers []string `json:"triggers"`
}

// UnmarshalJSON keeps argv entries strict: JSON null is not an empty argument.
func (c *OneshotChange) UnmarshalJSON(data []byte) error {
	var wire struct {
		Command  []json.RawMessage `json:"command"`
		Timeout  string            `json:"timeout"`
		Triggers []string          `json:"triggers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return errors.New("invalid oneshot JSON")
	}
	command := make([]string, len(wire.Command))
	for i, raw := range wire.Command {
		var argument *string
		if err := json.Unmarshal(raw, &argument); err != nil || argument == nil {
			return errors.New("oneshot command arguments must be strings")
		}
		command[i] = *argument
	}
	*c = OneshotChange{Command: command, Timeout: wire.Timeout, Triggers: wire.Triggers}
	return nil
}

var triggerPattern = regexp.MustCompile(`^(binary|config)\.[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Validate checks executable oneshot inputs without executing or resolving them.
func (c *OneshotChange) Validate() error {
	if c == nil || len(c.Command) == 0 || strings.TrimSpace(c.Command[0]) == "" || strings.HasPrefix(c.Command[0], "-") {
		return errors.New("oneshot command requires a nonempty executable")
	}
	for _, arg := range c.Command {
		if strings.ContainsRune(arg, '\x00') {
			return errors.New("oneshot command cannot contain NUL bytes")
		}
	}
	timeout, err := time.ParseDuration(c.Timeout)
	if err != nil || timeout <= 0 {
		return errors.New("invalid oneshot timeout")
	}
	if len(c.Triggers) == 0 {
		return errors.New("oneshot requires changed input triggers")
	}
	seen := map[string]bool{}
	for _, ref := range c.Triggers {
		if !triggerPattern.MatchString(ref) || seen[ref] {
			return errors.New("invalid or duplicate oneshot trigger")
		}
		seen[ref] = true
	}
	return nil
}

// Action is a concrete operation. Resolve is shared by display and execution.
type Action struct {
	Resume      bool
	Kind        string
	File        *FileChange
	RPM         *RPMChange
	Artifact    *ArtifactChange
	Process     *ProcessChange
	Oneshot     *OneshotChange
	Path        *PathChange
	Storage     *StorageChange
	Mount       *MountChange
	Account     *AccountChange
	Description []string
}
type Executor interface {
	Resolve(Task) ([]Action, error)
	Execute(context.Context, Action) error
}

// HostExecutor binds operations to a host without contacting it. Authentication
// and connection errors belong to execution and fail only the affected host.
type HostExecutor interface {
	ForHost(Host) (Executor, error)
}

// Bind is shared by preparation, display, and apply. Executors without host
// binding support unprivileged local plans.
func Bind(h Host, e Executor) (Executor, error) {
	if hostExecutor, ok := e.(HostExecutor); ok {
		return hostExecutor.ForHost(h)
	}
	if h.Transport != "local" || (h.Connection != nil && h.Connection.Sudo) {
		return nil, fmt.Errorf("executor does not support host %s connection", h.Name)
	}
	return e, nil
}

func (p *Plan) Validate() error {
	if p.Version != Version {
		return fmt.Errorf("incompatible plan version %q (required %s); prepare a new plan", p.Version, Version)
	}
	seenHosts := map[string]bool{}
	for _, h := range p.Hosts {
		if h.Name == "" || seenHosts[h.Name] {
			return errors.New("plan has an empty or duplicate host")
		}
		seenHosts[h.Name] = true
		if h.Transport != "local" && h.Transport != "ssh" {
			return fmt.Errorf("host %s: unsupported transport %q", h.Name, h.Transport)
		}
		if h.Connection != nil {
			c := h.Connection
			if c.Port < 0 || c.Port > 65535 {
				return fmt.Errorf("host %s: invalid connection port", h.Name)
			}
			if h.Transport == "local" && (c.Host != "" || c.User != "" || c.Port != 0) {
				return fmt.Errorf("host %s: local connection has SSH settings", h.Name)
			}
			if strings.ContainsAny(c.Host, "/@") || strings.HasPrefix(c.Host, "-") || strings.HasPrefix(c.User, "-") ||
				strings.IndexFunc(c.Host+c.User, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
				return fmt.Errorf("host %s: invalid connection host or user", h.Name)
			}
		}
		if h.Transport == "ssh" && (h.Connection == nil || h.Connection.Host == "") {
			return fmt.Errorf("host %s: SSH connection requires host", h.Name)
		}
		ids := map[string]bool{}
		automaticGroups := map[string]bool{}
		for _, t := range h.Tasks {
			if t.ID == "" || ids[t.ID] {
				return fmt.Errorf("host %s: empty or duplicate task ID", h.Name)
			}
			ids[t.ID] = true
			payloads := 0
			if t.Account != nil {
				payloads++
				if err := t.Account.Validate(); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
				for _, group := range t.Account.Groups {
					if group.GID == nil && !automaticGroups[group.Name] {
						return fmt.Errorf("task %s: group %s requires a preceding automatic group creation", t.ID, group.Name)
					}
				}
				if t.Account.Kind == "group" {
					automaticGroups[t.Account.Name] = t.Account.Operation == "create" && t.Account.GID == nil
				}
			}
			if t.Artifact != nil {
				payloads++
				if err := t.Artifact.Validate(); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
			}
			if t.File != nil {
				payloads++
				if err := t.File.ValidatePrepared(); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
			}
			if t.RPM != nil {
				payloads++
				if err := inventory.ValidateRPMDisableExcludes(t.RPM.DisableExcludes); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
			}
			if t.Process != nil {
				payloads++
			}
			if t.Oneshot != nil {
				payloads++
			}
			if t.Path != nil {
				payloads++
			}
			if t.Storage != nil {
				payloads++
			}
			if t.Mount != nil {
				payloads++
			}
			if payloads > 1 {
				return fmt.Errorf("task %s: mixed operation payloads", t.ID)
			}
			if (t.File != nil) != (t.Operation == "file") ||
				(t.Account != nil) != (t.Operation == "account") ||
				(t.Artifact != nil) != (t.Operation == "artifact") ||
				(t.Path != nil) != (t.Operation == "path") ||
				(t.Storage != nil) != (t.Operation == "storage") ||
				(t.Mount != nil) != (t.Operation == "mount") {
				return fmt.Errorf("task %s: resource payload does not match operation", t.ID)
			}
			if t.Path != nil {
				if err := t.Path.Validate(); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
			}
			if t.Mount != nil {
				if err := t.Mount.Validate(); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
			}
			if t.Storage != nil {
				if err := t.Storage.Validate(); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
			}
			if t.Operation == "oneshot" {
				if err := t.Oneshot.Validate(); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
			} else if t.Oneshot != nil {
				return fmt.Errorf("task %s: unexpected oneshot payload", t.ID)
			}
			switch t.Progress.Status {
			case "pending", "running", "success", "error":
			default:
				return fmt.Errorf("task %s: invalid progress status", t.ID)
			}
		}
	}
	return nil
}

func Display(w io.Writer, p *Plan, e Executor) error {
	var output strings.Builder
	if err := display(&output, p, e); err != nil {
		return err
	}
	_, err := io.WriteString(w, output.String())
	return err
}
func display(w io.Writer, p *Plan, e Executor) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for _, h := range p.Hosts {
		hostExecutor, err := Bind(h, e)
		if err != nil {
			return fmt.Errorf("host %s: %w", h.Name, err)
		}
		if _, err := fmt.Fprintf(w, "Host %s (%s)\n", h.Name, h.Transport); err != nil {
			return err
		}
		if c := h.Connection; c != nil {
			fmt.Fprint(w, "  Connection:")
			if c.Host != "" {
				fmt.Fprintf(w, " host=%s", c.Host)
			}
			if c.User != "" {
				fmt.Fprintf(w, " user=%s", c.User)
			}
			if c.Port != 0 {
				fmt.Fprintf(w, " port=%d", c.Port)
			}
			fmt.Fprintf(w, " sudo=%t\n", c.Sudo)
		}
		if h.Transport == "ssh" {
			fmt.Fprintln(w, "  SSH uses current client configuration; explicit connection settings take precedence")
		}
		if len(h.Tasks) == 0 {
			fmt.Fprintln(w, "  No changes")
		}
		for _, t := range h.Tasks {
			actions, err := hostExecutor.Resolve(t)
			if err != nil {
				if t.File != nil && t.File.Sensitive {
					err = errors.New("sensitive file operation is invalid")
				}
				return fmt.Errorf("task %s: %w", t.ID, err)
			}
			fmt.Fprintf(w, "  %s/%s [%s]\n", t.Service, t.Component, t.Progress.Status)
			for _, a := range actions {
				if len(a.Description) > 0 && !(t.File != nil && t.File.Sensitive) {
					for _, line := range a.Description {
						fmt.Fprintf(w, "    %s\n", line)
					}
					continue
				}
				if a.File == nil {
					return errors.New("action has no file")
				}
				f := a.File
				fmt.Fprintf(w, "    %s %s", a.Kind, f.Path)
				if f.Owner != "" {
					fmt.Fprintf(w, " owner=%s", f.Owner)
				}
				if f.Group != "" {
					fmt.Fprintf(w, " group=%s", f.Group)
				}
				if f.Mode != nil {
					fmt.Fprintf(w, " mode=%04o", *f.Mode)
				}
				if f.SELinux != "" {
					fmt.Fprintf(w, " selinux=%s", f.SELinux)
				}
				if a.Kind == "overwrite-file" {
					if f.AtomicWrites {
						fmt.Fprint(w, " atomic")
					} else {
						fmt.Fprint(w, " non-atomic")
					}
				}
				if f.BackupPath != "" {
					fmt.Fprintf(w, " backup=%s", f.BackupPath)
				}
				fmt.Fprintln(w)
				if a.Kind == "overwrite-file" && f.Expected != nil && f.Expected.Kind == "symlink" {
					fmt.Fprintln(w, "      replace final symlink with regular file")
				}
				if a.Kind == "overwrite-file" && f.Validate != nil {
					if f.Sensitive {
						fmt.Fprintf(w, "      validate candidate before write (arguments hidden: sensitive); timeout %s\n", f.Validate.Timeout)
					} else {
						args := make([]string, len(f.Validate.Args))
						for i, arg := range f.Validate.Args {
							args[i] = strconv.Quote(arg)
						}
						fmt.Fprintf(w, "      validate candidate before write: [%s]; timeout %s\n", strings.Join(args, ", "), f.Validate.Timeout)
					}
				}
			}
			if t.File != nil {
				if t.File.Sensitive {
					fmt.Fprintln(w, "    Content hidden: sensitive")
				} else {
					fmt.Fprint(w, t.File.Patch)
				}
			}
		}
	}
	return nil
}

type TaskResult struct {
	ID     string
	Status string
	Error  string
}
type HostResult struct {
	Name  string
	Tasks []TaskResult
}
type Result struct {
	Hosts    []HostResult
	PlanPath string
}

func DisplayResult(w io.Writer, r Result) error {
	var output strings.Builder
	if err := displayResult(&output, r); err != nil {
		return err
	}
	_, err := io.WriteString(w, output.String())
	return err
}
func displayResult(w io.Writer, r Result) error {
	if _, err := fmt.Fprintln(w, "Result:"); err != nil {
		return err
	}
	for _, h := range r.Hosts {
		fmt.Fprintf(w, "  %s:", h.Name)
		if len(h.Tasks) == 0 {
			fmt.Fprint(w, " no changes")
		}
		fmt.Fprintln(w)
		for _, t := range h.Tasks {
			fmt.Fprintf(w, "    %s: %s", t.ID, t.Status)
			if t.Error != "" {
				fmt.Fprintf(w, " (%s)", t.Error)
			}
			fmt.Fprintln(w)
		}
	}
	if r.PlanPath != "" {
		fmt.Fprintf(w, "Plan: %s; run apply with this file to continue unfinished tasks.\n", r.PlanPath)
	}
	return nil
}
