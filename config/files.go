// Package config prepares templates and resolves concrete local file operations.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/subuk/richt/facts"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
	"github.com/subuk/richt/secrets"
	"github.com/subuk/richt/source"
)

// Diff renders all templates and validates capabilities before observing resources.
func Diff(r *inventory.Resolved) (*plan.Plan, error) {
	if r == nil {
		return nil, errors.New("resolved inventory is nil")
	}
	lookup := secrets.New(r.SecretSources)
	var sources *source.Client
	p := &plan.Plan{Version: plan.Version}
	for _, h := range r.Hosts {
		if h.Transport.Type != "local" || h.Transport.Sudo {
			return nil, fmt.Errorf("host %s: transport %s/sudo is not supported yet", h.Name, h.Transport.Type)
		}
		ph := plan.Host{Name: h.Name, Transport: "local"}
		for _, s := range h.Services {
			if len(s.Definition.Binaries) > 0 || len(s.Definition.Processes) > 0 {
				return nil, fmt.Errorf("host %s service %s: binary/process backends are not supported yet", h.Name, s.Name)
			}
			for position := range s.Definition.Configs {
				index := position
				if s.State == "absent" {
					index = len(s.Definition.Configs) - 1 - position
				}
				c := s.Definition.Configs[index]
				label := c.Name
				if label == "" {
					label = fmt.Sprintf("[%d]", index+1)
				}
				if sources == nil && c.Template.URL != "" && s.State != "absent" {
					var err error
					sources, err = source.New(source.Options{})
					if err != nil {
						return nil, templateReadError(c.Sensitive, err)
					}
				}
				f, err := PrepareContext(context.Background(), c, s.State, h.Vars, sources, lookup)
				if err != nil {
					return nil, fmt.Errorf("host %s service %s config %s: %w", h.Name, s.Name, label, err)
				}
				ph.Tasks = append(ph.Tasks, plan.Task{ID: fmt.Sprintf("%s/config/%d", s.Name, index), Service: s.Name, Component: "config/" + label, Operation: "file", File: f, Progress: plan.Progress{Status: "pending"}})
			}
		}
		p.Hosts = append(p.Hosts, ph)
	}
	// Only after every template/capability is valid, read current files and keep changes.
	for hi := range p.Hosts {
		h := &p.Hosts[hi]
		tasks := h.Tasks[:0]
		for _, t := range h.Tasks {
			changed, err := Observe(t.File)
			if err != nil {
				if t.File.Sensitive {
					err = errors.New("sensitive file observation failed")
				}
				return nil, fmt.Errorf("host %s config %s: %w", h.Name, t.Component, err)
			}
			if changed {
				tasks = append(tasks, t)
			}
		}
		h.Tasks = tasks
	}
	return p, p.Validate()
}

// SecretLookup retrieves a string from the explicitly selected secret source.
type SecretLookup interface {
	Lookup(key, source string) (string, error)
}

// PrepareOptions supplies controller sources and host-scoped template lookups.
// Fact is evaluated only by an executed fact call, never while parsing.
type PrepareOptions struct {
	Sources *source.Client
	Secrets SecretLookup
	Fact    func(context.Context, string) (any, error)
}

// Prepare freezes rendered content and requested ownership names for a file task.
// The optional lookup is shared across all preparations in a diff.
func Prepare(c inventory.Config, state string, vars map[string]any, lookups ...SecretLookup) (*plan.FileChange, error) {
	return PrepareContext(context.Background(), c, state, vars, nil, lookups...)
}

// PrepareContext acquires and renders a template using the caller's context.
// Sources may be shared across preparations to freeze remote template bytes.
// A nil source client is initialized only when a remote template is needed.
func PrepareContext(ctx context.Context, c inventory.Config, state string, vars map[string]any, sources *source.Client, lookups ...SecretLookup) (*plan.FileChange, error) {
	options := PrepareOptions{Sources: sources}
	if len(lookups) > 0 {
		options.Secrets = lookups[0]
	}
	return PrepareWithOptions(ctx, c, state, vars, options)
}

// PrepareWithOptions freezes rendered inputs using optional host facts and
// secrets. Absent assignments do not acquire templates or invoke lookups.
func PrepareWithOptions(ctx context.Context, c inventory.Config, state string, vars map[string]any, options PrepareOptions) (*plan.FileChange, error) {
	if c.Type != "" && c.Type != "template" {
		return nil, errors.New("config type: expected template")
	}
	if state == "" {
		state = "present"
	}
	if state != "present" && state != "absent" {
		return nil, fmt.Errorf("invalid file state %q", state)
	}
	if !filepath.IsAbs(c.Path) || filepath.Clean(c.Path) == "/" {
		return nil, errors.New("file path must be absolute and identify a file")
	}
	f := &plan.FileChange{Path: filepath.Clean(c.Path), State: state, Sensitive: c.Sensitive, SELinux: c.SELinux, AtomicWrites: true, Backup: c.Backup}
	if c.AtomicWrites != nil {
		f.AtomicWrites = *c.AtomicWrites
	}
	f.Owner, f.Group = c.Owner, c.Group
	if c.Validate != nil {
		v := &plan.FileValidator{Args: append([]string(nil), c.Validate.Args...), Timeout: c.Validate.Timeout}
		if v.Timeout == "" {
			v.Timeout = "5s"
		}
		if _, err := validatorDuration(v); err != nil {
			return nil, err
		}
		if state == "present" {
			f.Validate = v
		}
	}
	if c.Mode != "" {
		m, err := strconv.ParseUint(c.Mode, 8, 12)
		if err != nil {
			return nil, fmt.Errorf("invalid file mode %q", c.Mode)
		}
		mode := uint32(m)
		f.Mode = &mode
	}
	if c.SELinux != "" {
		if err := validateLabelSyntax(c.SELinux); err != nil {
			return nil, err
		}
	}
	if state == "absent" {
		return f, nil
	}
	text, err := acquireTemplate(ctx, c.Template, options.Sources)
	if err != nil {
		return nil, templateReadError(c.Sensitive, err)
	}
	if err := renderTemplate(ctx, f, text, vars, options); err != nil {
		return nil, err
	}
	return f, nil
}

func acquireTemplate(ctx context.Context, t inventory.Template, sources *source.Client) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := inventory.ValidateTemplate(t); err != nil {
		return "", err
	}
	if t.Inline != nil {
		return *t.Inline, nil
	}
	if t.URL != "" {
		if sources == nil {
			var err error
			sources, err = source.New(source.Options{})
			if err != nil {
				return "", err
			}
		}
		document, err := sources.Read(ctx, t.URL, t.SHA256)
		return string(document.Data), err
	}
	b, err := os.ReadFile(t.File)
	return string(b), err
}

func templateReadError(sensitive bool, err error) error {
	if sensitive {
		return errors.New("read sensitive template failed")
	}
	return fmt.Errorf("read template: %w", err)
}

func renderTemplate(ctx context.Context, f *plan.FileChange, text string, vars map[string]any, options PrepareOptions) error {
	var secretFailure string
	var factFailure string
	failSecret := func(message string) (string, error) {
		secretFailure = message
		return "", errors.New(message)
	}
	funcs := template.FuncMap{"secret": func(args ...any) (string, error) {
		// Mark even failed calls before checking arguments. Runtime template errors
		// may quote arguments, source expressions, or transformed secret values.
		f.Sensitive = true
		if len(args) < 1 || len(args) > 2 {
			return failSecret("secret requires a key and optional source")
		}
		key, ok := args[0].(string)
		if !ok {
			return failSecret("secret key must be a string")
		}
		source := "default"
		if len(args) == 2 {
			source, ok = args[1].(string)
			if !ok {
				return failSecret("secret source must be a string")
			}
		}
		if options.Secrets == nil {
			return failSecret("secret lookup is unavailable")
		}
		value, err := options.Secrets.Lookup(key, source)
		if err != nil {
			return failSecret("secret lookup failed")
		}
		return value, nil
	}}
	funcs["fact"] = func(path string) (any, error) {
		fail := func(message string) (any, error) {
			factFailure = message
			return nil, errors.New(message)
		}
		if err := facts.ValidatePath(path); err != nil {
			return fail("unknown fact path")
		}
		if options.Fact == nil {
			return fail("fact lookup is unavailable")
		}
		value, err := options.Fact(ctx, path)
		if err != nil {
			if errors.Is(err, facts.ErrCacheIO) {
				return fail("fact cache I/O failed; use --no-facts-cache to disable persistent caching")
			}
			return fail("fact lookup failed")
		}
		return value, nil
	}
	funcs["index"] = templateIndex
	tpl, err := template.New("config").Option("missingkey=error").Funcs(funcs).Parse(text)
	if err != nil {
		// Parser errors can include arbitrary template text before a secret call
		// can run, so never expose the underlying diagnostic.
		return errors.New("render template: parsing failed")
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, vars); err != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if secretFailure != "" {
			return errors.New("render template: " + secretFailure)
		}
		if factFailure != "" {
			return errors.New("render template: " + factFailure)
		}
		return errors.New("render template: execution failed")
	}
	f.Content = b.String()
	return nil
}

// Observe computes the final patch; apply never invokes it.
func Observe(f *plan.FileChange, anticipated ...Ownership) (changed bool, err error) {
	defer func() {
		if err != nil && f != nil && f.Sensitive {
			err = errors.New("sensitive file observation failed")
		}
	}()
	projection := projectedOwnership(anticipated)
	uid, gid := -1, -1
	if f.State != "absent" {
		uid, gid, err = projection.local(f)
		if err != nil {
			return false, err
		}
	}
	if f.SELinux != "" {
		if err := validateLabel(f.SELinux); err != nil {
			return false, err
		}
	}
	state, old, err := observeEntry(context.Background(), localFileInput, f)
	if err != nil {
		return false, err
	}
	return entryChanged(f, state, old, uid, gid, projection.pending())
}

// ObserveMissing prepares a file known to become absent before its file task,
// such as an owned unit after its explicitly requested unmask operation.
func ObserveMissing(f *plan.FileChange, anticipated ...Ownership) (changed bool, err error) {
	defer func() {
		if err != nil && f != nil && f.Sensitive {
			err = errors.New("sensitive file observation failed")
		}
	}()
	if _, err := (Local{}).Resolve(plan.Task{Operation: "file", File: f}); err != nil {
		return false, err
	}
	if f.State != "present" {
		return false, errors.New("missing file observation requires present state")
	}
	if _, _, err := projectedOwnership(anticipated).local(f); err != nil {
		return false, err
	}
	if f.SELinux != "" {
		if err := validateLabel(f.SELinux); err != nil {
			return false, err
		}
	}
	f.Write = true
	return true, freezeFile(f, &plan.FileState{Kind: "absent"}, nil)
}

func validateLabelSyntax(label string) error {
	parts := strings.SplitN(label, ":", 4)
	if len(parts) < 3 || strings.ContainsAny(label, " \t\n\r\x00") {
		return errors.New("invalid SELinux context")
	}
	for _, part := range parts {
		if part == "" {
			return errors.New("invalid SELinux context")
		}
	}
	return nil
}

// Names are resolved on the managed host each time, rather than saved as IDs.
func ownership(f *plan.FileChange) (int, int, error) {
	uid, gid := -1, -1
	if f.Owner != "" {
		u, err := user.Lookup(f.Owner)
		if err != nil {
			return 0, 0, fmt.Errorf("owner %q does not exist", f.Owner)
		}
		uid, err = strconv.Atoi(u.Uid)
		if err != nil {
			return 0, 0, err
		}
	}
	if f.Group != "" {
		g, err := user.LookupGroup(f.Group)
		if err != nil {
			return 0, 0, fmt.Errorf("group %q does not exist", f.Group)
		}
		gid, err = strconv.Atoi(g.Gid)
		if err != nil {
			return 0, 0, err
		}
	}
	return uid, gid, nil
}

type Local struct{}

func (Local) Resolve(t plan.Task) ([]plan.Action, error) {
	if t.Operation != "file" || t.Account != nil || t.Artifact != nil || t.File == nil || t.RPM != nil || t.Process != nil || t.Oneshot != nil || t.DEB != nil {
		return nil, fmt.Errorf("unsupported operation %q", t.Operation)
	}
	f := t.File
	if !filepath.IsAbs(f.Path) || filepath.Clean(f.Path) != f.Path || f.Path == "/" {
		return nil, errors.New("invalid file path in plan")
	}
	if f.Mode != nil && *f.Mode > 07777 {
		return nil, errors.New("invalid file attributes in plan")
	}
	if _, err := validatorDuration(f.Validate); err != nil {
		return nil, err
	}
	resume := t.Progress.Status == "running" || t.Progress.Status == "error"
	if f.State == "absent" {
		return []plan.Action{{Kind: "remove-file", File: f, Resume: resume}}, nil
	}
	if f.State != "present" {
		return nil, errors.New("invalid file state in plan")
	}
	if f.SELinux != "" {
		if err := validateLabelSyntax(f.SELinux); err != nil {
			return nil, err
		}
	}
	if f.Write {
		return []plan.Action{{Kind: "overwrite-file", File: f, Resume: resume}}, nil
	}
	return []plan.Action{{Kind: "set-file-attributes", File: f, Resume: resume}}, nil
}

func (Local) Execute(ctx context.Context, a plan.Action) (err error) {
	defer func() {
		if err != nil && a.File != nil && a.File.Sensitive {
			err = errors.New("sensitive file operation failed")
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	f := a.File
	if f == nil {
		return errors.New("missing file action inputs")
	}
	if _, err := (Local{}).Resolve(plan.Task{Operation: "file", File: f}); err != nil {
		return err
	}
	if err := f.ValidatePrepared(); err != nil {
		return err
	}
	if a.Kind == "create-parent-directories" {
		return os.MkdirAll(filepath.Dir(f.Path), 0755)
	}
	if a.Kind != "overwrite-file" && a.Kind != "set-file-attributes" && a.Kind != "remove-file" {
		return fmt.Errorf("unsupported action %q", a.Kind)
	}
	if a.Kind == "overwrite-file" && f.Write && f.Validate != nil {
		if err := validateLocalCandidate(ctx, f); err != nil {
			return err
		}
	}
	uid, gid := -1, -1
	if a.Kind != "remove-file" {
		uid, gid, err = ownership(f)
		if err != nil {
			return err
		}
	}
	_, err = fileCommand(ctx, localFileInput, fileRequest{Action: a.Kind, File: f, Resume: a.Resume, UID: uid, GID: gid})
	return err
}
