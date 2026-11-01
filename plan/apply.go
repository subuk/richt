package plan

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const DefaultHostConcurrency = 20

// ApplyOptions configures execution, without changing the prepared plan.
type ApplyOptions struct {
	HostConcurrency int
}

// Apply executes each host sequentially, with bounded concurrency across hosts.
// Every progress mutation and persistence call is serialized. A persistence
// failure stops new action admissions; already admitted actions are awaited and
// their final progress is recorded before returning.
func Apply(ctx context.Context, p *Plan, e Executor, persist func(*Plan) error, options ...ApplyOptions) (Result, error) {
	result := Result{}
	limit := DefaultHostConcurrency
	if len(options) > 1 {
		return result, errors.New("apply accepts at most one options value")
	}
	if len(options) == 1 {
		limit = options[0].HostConcurrency
		if limit <= 0 {
			return result, errors.New("host concurrency must be positive")
		}
	}
	if err := p.Validate(); err != nil {
		return result, err
	}
	// Validate every concrete operation before the first host can change state.
	resolved := make([][][]Action, len(p.Hosts))
	executors := make([]Executor, len(p.Hosts))
	result.Hosts = make([]HostResult, len(p.Hosts))
	for hi, h := range p.Hosts {
		bound, err := Bind(h, e)
		if err != nil {
			return Result{}, fmt.Errorf("host %s: %w", h.Name, err)
		}
		executors[hi] = bound
		resolved[hi] = make([][]Action, len(h.Tasks))
		result.Hosts[hi] = HostResult{Name: h.Name, Tasks: make([]TaskResult, len(h.Tasks))}
		for ti, t := range h.Tasks {
			actions, err := bound.Resolve(t)
			if err != nil {
				if t.File != nil && t.File.Sensitive {
					err = errors.New("sensitive file operation is invalid")
				}
				return Result{}, fmt.Errorf("host %s task %s: %w", h.Name, t.ID, err)
			}
			resolved[hi][ti] = actions
		}
	}

	// This mutex covers progress, serialization of its snapshot, and admission of
	// each action. Execution itself runs outside the mutex.
	var mu sync.Mutex
	var saveErr error
	var contextErr error
	hostErrors := make([]error, len(p.Hosts))
	save := func() {
		if persist != nil {
			if err := persist(p); err != nil && saveErr == nil {
				saveErr = fmt.Errorf("save progress: %w", err)
			}
		}
	}
	stop := func() string {
		if saveErr != nil {
			return saveErr.Error()
		}
		if err := ctx.Err(); err != nil {
			contextErr = err
			return err.Error()
		}
		return ""
	}
	runHost := func(hi int) {
		h := &p.Hosts[hi]
		blocked := ""
		for ti := range h.Tasks {
			mu.Lock()
			t := &h.Tasks[ti]
			tr := TaskResult{ID: t.ID}
			if t.Progress.Status == "success" {
				tr.Status = "previously completed"
				result.Hosts[hi].Tasks[ti] = tr
				mu.Unlock()
				continue
			}
			if reason := stop(); reason != "" {
				blocked = reason
			}
			if blocked != "" {
				tr.Status, tr.Error = "blocked", blocked
				result.Hosts[hi].Tasks[ti] = tr
				mu.Unlock()
				continue
			}
			t.Progress = Progress{Status: "running"}
			save()
			if reason := stop(); reason != "" {
				blocked = reason
				tr.Status, tr.Error = "blocked", reason
				result.Hosts[hi].Tasks[ti] = tr
				mu.Unlock()
				continue
			}
			mu.Unlock()

			var runErr error
			for _, action := range resolved[hi][ti] {
				mu.Lock()
				reason := stop()
				mu.Unlock()
				if reason != "" {
					runErr = errors.New(reason)
					break
				}
				if runErr = executors[hi].Execute(ctx, action); runErr != nil {
					break
				}
			}

			mu.Lock()
			if runErr != nil {
				message := runErr.Error()
				if t.File != nil && t.File.Sensitive {
					message = "sensitive file operation failed"
				}
				t.Progress = Progress{Status: "error", Error: message}
				blocked = message
				hostErrors[hi] = fmt.Errorf("host %s task %s: %s", h.Name, t.ID, message)
			} else {
				t.Progress = Progress{Status: "success"}
			}
			// Even after an earlier save failed, await active actions and attempt
			// to persist their final status. The first failure still stops work.
			save()
			tr.Status, tr.Error = t.Progress.Status, t.Progress.Error
			result.Hosts[hi].Tasks[ti] = tr
			mu.Unlock()
		}
	}

	if limit > len(p.Hosts) {
		limit = len(p.Hosts)
	}
	var workers sync.WaitGroup
	next := 0
	for range limit {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				mu.Lock()
				if next == len(p.Hosts) {
					mu.Unlock()
					return
				}
				hi := next
				next++
				mu.Unlock()
				runHost(hi)
			}
		}()
	}
	workers.Wait()
	if contextErr == nil {
		contextErr = ctx.Err()
	}
	return result, errors.Join(append(hostErrors, saveErr, contextErr)...)
}
