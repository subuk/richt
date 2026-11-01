// Package plan defines executable changes and their resumable progress.
package plan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const Version = "1.0"

type Plan struct {
	Version string `json:"version"`
	Hosts   []Host `json:"hosts"`
}
type Host struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Tasks     []Task `json:"tasks"`
}
type Task struct {
	ID        string         `json:"id"`
	Service   string         `json:"service"`
	Component string         `json:"component"`
	Operation string         `json:"operation"`
	File      *FileChange    `json:"file,omitempty"`
	RPM       *RPMChange     `json:"rpm,omitempty"`
	Process   *ProcessChange `json:"process,omitempty"`
	Progress  Progress       `json:"progress"`
}
type FileChange struct {
	Path      string  `json:"path"`
	State     string  `json:"state"`
	Content   string  `json:"content,omitempty"`
	Patch     string  `json:"patch,omitempty"`
	Sensitive bool    `json:"sensitive,omitempty"`
	Write     bool    `json:"write,omitempty"`
	UID       *int    `json:"uid,omitempty"`
	GID       *int    `json:"gid,omitempty"`
	Mode      *uint32 `json:"mode,omitempty"`
	SELinux   string  `json:"selinux,omitempty"`
}
type Progress struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// RPMChange fixes a package request, while the target resolves repository dependencies.
type RPMChange struct {
	Manager string `json:"manager"`
	Package string `json:"package"`
	Version string `json:"version,omitempty"`
	State   string `json:"state"`
}

// ProcessChange contains both permitted branches; apply only selects a branch.
type ProcessChange struct {
	Unit      string `json:"unit"`
	State     string `json:"state"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Timeout   string `json:"timeout"`
	Reaction  string `json:"reaction,omitempty"`
	MissingOK bool   `json:"missing_ok,omitempty"`
}

// Action is a concrete file operation. Resolve is shared by display and execution.
type Action struct {
	Kind        string
	File        *FileChange
	RPM         *RPMChange
	Process     *ProcessChange
	Description []string
}
type Executor interface {
	Resolve(Task) ([]Action, error)
	Execute(context.Context, Action) error
}

func (p *Plan) Validate() error {
	if p.Version != Version {
		return fmt.Errorf("incompatible plan version %q (supported %s)", p.Version, Version)
	}
	seenHosts := map[string]bool{}
	for _, h := range p.Hosts {
		if h.Name == "" || seenHosts[h.Name] {
			return errors.New("plan has an empty or duplicate host")
		}
		seenHosts[h.Name] = true
		if h.Transport != "local" {
			return fmt.Errorf("host %s: unsupported transport %q", h.Name, h.Transport)
		}
		ids := map[string]bool{}
		for _, t := range h.Tasks {
			if t.ID == "" || ids[t.ID] {
				return fmt.Errorf("host %s: empty or duplicate task ID", h.Name)
			}
			ids[t.ID] = true
			payloads := 0
			if t.File != nil {
				payloads++
			}
			if t.RPM != nil {
				payloads++
			}
			if t.Process != nil {
				payloads++
			}
			if payloads > 1 {
				return fmt.Errorf("task %s: mixed operation payloads", t.ID)
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
		if _, err := fmt.Fprintf(w, "Host %s (%s)\n", h.Name, h.Transport); err != nil {
			return err
		}
		if len(h.Tasks) == 0 {
			fmt.Fprintln(w, "  No changes")
		}
		for _, t := range h.Tasks {
			actions, err := e.Resolve(t)
			if err != nil {
				return fmt.Errorf("task %s: %w", t.ID, err)
			}
			fmt.Fprintf(w, "  %s/%s [%s]\n", t.Service, t.Component, t.Progress.Status)
			for _, a := range actions {
				if len(a.Description) > 0 {
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
				if f.UID != nil {
					fmt.Fprintf(w, " uid=%d", *f.UID)
				}
				if f.GID != nil {
					fmt.Fprintf(w, " gid=%d", *f.GID)
				}
				if f.Mode != nil {
					fmt.Fprintf(w, " mode=%04o", *f.Mode)
				}
				if f.SELinux != "" {
					fmt.Fprintf(w, " selinux=%s", f.SELinux)
				}
				fmt.Fprintln(w)
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

// Apply never observes resources to rebuild a plan. A failed host does not block others.
// A persistence error stops the entire run before any further action starts.
func Apply(ctx context.Context, p *Plan, e Executor, persist func(*Plan) error) (Result, error) {
	result := Result{}
	if err := p.Validate(); err != nil {
		return result, err
	}
	// Resolve everything before performing any mutation, including unsupported operations.
	resolved := make([][][]Action, len(p.Hosts))
	for hi, h := range p.Hosts {
		resolved[hi] = make([][]Action, len(h.Tasks))
		for ti, t := range h.Tasks {
			a, err := e.Resolve(t)
			if err != nil {
				return result, fmt.Errorf("host %s task %s: %w", h.Name, t.ID, err)
			}
			resolved[hi][ti] = a
		}
	}
	var errs []error
	globalStop := ""
	save := func() error {
		if persist == nil {
			return nil
		}
		if err := persist(p); err != nil {
			return fmt.Errorf("save progress: %w", err)
		}
		return nil
	}
	for hi := range p.Hosts {
		h := &p.Hosts[hi]
		hr := HostResult{Name: h.Name}
		blocked := globalStop
		for ti := range h.Tasks {
			t := &h.Tasks[ti]
			if t.Progress.Status == "success" {
				hr.Tasks = append(hr.Tasks, TaskResult{ID: t.ID, Status: "previously completed"})
				continue
			}
			if blocked != "" {
				hr.Tasks = append(hr.Tasks, TaskResult{ID: t.ID, Status: "blocked", Error: blocked})
				continue
			}
			if err := ctx.Err(); err != nil {
				blocked = err.Error()
				errs = append(errs, err)
				hr.Tasks = append(hr.Tasks, TaskResult{ID: t.ID, Status: "blocked", Error: blocked})
				continue
			}
			t.Progress = Progress{Status: "running"}
			if err := save(); err != nil {
				globalStop = err.Error()
				blocked = globalStop
				errs = append(errs, err)
				hr.Tasks = append(hr.Tasks, TaskResult{ID: t.ID, Status: "blocked", Error: blocked})
				continue
			}
			var runErr error
			for _, a := range resolved[hi][ti] {
				if runErr = e.Execute(ctx, a); runErr != nil {
					break
				}
			}
			if runErr != nil {
				message := runErr.Error()
				if t.File != nil && t.File.Sensitive {
					message = "sensitive file operation failed"
				}
				t.Progress = Progress{Status: "error", Error: message}
				blocked = message
				errs = append(errs, fmt.Errorf("host %s task %s: %s", h.Name, t.ID, message))
			} else {
				t.Progress = Progress{Status: "success"}
			}
			if err := save(); err != nil {
				globalStop = err.Error()
				blocked = globalStop
				errs = append(errs, err)
			}
			hr.Tasks = append(hr.Tasks, TaskResult{ID: t.ID, Status: t.Progress.Status, Error: t.Progress.Error})
		}
		result.Hosts = append(result.Hosts, hr)
	}
	return result, errors.Join(errs...)
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
