package plan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const DefaultHostConcurrency = 20

// ApplyOptions configures execution, without changing the prepared plan.
type ApplyOptions struct {
	// HostConcurrency must be positive when options are supplied.
	HostConcurrency int
	// Observer receives serialized lifecycle snapshots synchronously, outside
	// the progress lock. A slow observer applies backpressure to its workers,
	// but cannot prevent another active worker from saving progress. Observers
	// must not mutate the plan or call Apply recursively with the same plan.
	Observer func(Event)
}

// Apply executes each host sequentially, with bounded concurrency across hosts.
// Every progress mutation and persistence call is serialized. A persistence
// failure stops new action admissions; already admitted actions are awaited and
// their final progress is recorded before returning.
func Apply(ctx context.Context, p *Plan, e Executor, persist func(*Plan) error, options ...ApplyOptions) (result Result, applyErr error) {
	limit := DefaultHostConcurrency
	var observer func(Event)
	if len(options) > 1 {
		return result, errors.New("apply accepts at most one options value")
	}
	if len(options) == 1 {
		limit = options[0].HostConcurrency
		if limit <= 0 {
			return result, errors.New("host concurrency must be positive")
		}
		observer = options[0].Observer
	}
	var eventMu sync.Mutex
	emit := func(event Event) {
		if observer == nil {
			return
		}
		eventMu.Lock()
		defer eventMu.Unlock()
		event.Time = time.Now()
		observer(event)
	}
	total := 0
	if p != nil {
		for _, h := range p.Hosts {
			total += len(h.Tasks)
		}
	}
	persistence := PersistenceNotRequested
	emit(Event{Kind: EventApplyStarted, Total: total, Persistence: persistence})
	var cancelOnce sync.Once
	emitCanceled := func() {
		cancelOnce.Do(func() {
			emit(Event{Kind: EventApplyCanceled, Error: "apply canceled", Total: total, Persistence: PersistenceNotRequested})
		})
	}
	stopObserving := make(chan struct{})
	var cancellationObserver sync.WaitGroup
	if observer != nil {
		cancellationObserver.Add(1)
		go func() {
			defer cancellationObserver.Done()
			select {
			case <-ctx.Done():
				emitCanceled()
			case <-stopObserving:
			}
		}()
	}
	defer func() {
		close(stopObserving)
		cancellationObserver.Wait()
		status, message := "success", ""
		if applyErr != nil {
			status, message = "error", "apply failed"
			if errors.Is(applyErr, context.Canceled) || errors.Is(applyErr, context.DeadlineExceeded) {
				status = "canceled"
				emitCanceled()
			}
		}
		emit(Event{Kind: EventApplyFinished, Status: status, Error: message, Total: total, Persistence: persistence})
	}()
	if p == nil {
		return result, errors.New("plan is nil")
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
	save := func() (PersistenceStatus, bool) {
		if persist != nil {
			if err := persist(p); err != nil {
				if saveErr == nil {
					saveErr = fmt.Errorf("save progress: %w", err)
				}
				persistence = PersistenceFailed
				return PersistenceFailed, true
			}
			if persistence != PersistenceFailed {
				persistence = PersistenceSaved
			}
			return PersistenceSaved, false
		}
		return PersistenceNotRequested, false
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
				emit(taskEvent(EventTaskSkipped, h.Name, *t))
				continue
			}
			if reason := stop(); reason != "" {
				blocked = reason
			}
			if blocked != "" {
				tr.Status, tr.Error = "blocked", blocked
				result.Hosts[hi].Tasks[ti] = tr
				event := taskEvent(EventTaskBlocked, h.Name, *t)
				event.Error = blocked
				if saveErr != nil {
					event.Error = "save progress failed"
				}
				mu.Unlock()
				emit(event)
				continue
			}
			t.Progress = Progress{Status: "running"}
			saved, failed := save()
			event := taskEvent(EventTaskStarted, h.Name, *t)
			event.Persistence = saved
			if reason := stop(); reason != "" {
				blocked = reason
				tr.Status, tr.Error = "blocked", reason
				result.Hosts[hi].Tasks[ti] = tr
				event.Kind, event.Error = EventTaskBlocked, reason
				if saveErr != nil {
					event.Error = "save progress failed"
				}
				mu.Unlock()
				if failed {
					failure := event
					failure.Kind, failure.Error = EventPersistenceFailed, "save progress failed"
					emit(failure)
				}
				emit(event)
				continue
			}
			mu.Unlock()
			emit(event)

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
			saved, failed = save()
			tr.Status, tr.Error = t.Progress.Status, t.Progress.Error
			result.Hosts[hi].Tasks[ti] = tr
			event = taskEvent(EventTaskSucceeded, h.Name, *t)
			event.Persistence, event.Error = saved, tr.Error
			if runErr != nil {
				event.Kind = EventTaskFailed
				// An admission failure can originate from an arbitrary persistence
				// callback, whose error may contain prepared secret bytes.
				if saveErr != nil {
					event.Error = "task stopped after progress save failure"
				}
			}
			mu.Unlock()
			emit(event)
			if failed {
				event.Kind, event.Error = EventPersistenceFailed, "save progress failed"
				emit(event)
			}
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
