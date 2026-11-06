package plan

import "time"

// EventKind identifies an apply lifecycle transition.
type EventKind string

const (
	EventApplyStarted      EventKind = "apply_started"
	EventTaskStarted       EventKind = "task_started"
	EventTaskSucceeded     EventKind = "task_succeeded"
	EventTaskFailed        EventKind = "task_failed"
	EventTaskSkipped       EventKind = "task_skipped"
	EventTaskBlocked       EventKind = "task_blocked"
	EventApplyCanceled     EventKind = "apply_canceled"
	EventPersistenceFailed EventKind = "persistence_failed"
	EventApplyFinished     EventKind = "apply_finished"
)

// PersistenceStatus describes the save associated with a transition. A task's
// execution result and the durability of its progress are independent.
type PersistenceStatus string

const (
	PersistenceNotRequested PersistenceStatus = "not_requested"
	PersistenceSaved        PersistenceStatus = "saved"
	PersistenceFailed       PersistenceStatus = "failed"
)

// Event is a detached, display-safe snapshot. It never contains prepared
// payloads, command arguments, command output, or persisted error objects.
// Total on apply lifecycle events is the number of tasks in this invocation,
// including completed tasks; task events leave it zero.
// Status on EventApplyFinished is success, error, or canceled.
// Cancellation is reported when the context is canceled, before awaiting active
// actions; its Persistence is not_requested because it does not save progress.
// Task errors use the same sensitive-file redaction as Result. Persistence and
// final errors are generic because those errors may include prepared payloads.
type Event struct {
	Kind        EventKind
	Time        time.Time
	Host        string
	TaskID      string
	Service     string
	Component   string
	Operation   string
	Error       string
	Total       int
	Persistence PersistenceStatus
	Status      string
}

func taskEvent(kind EventKind, host string, t Task) Event {
	return Event{
		Kind: kind, Host: host, TaskID: t.ID, Service: t.Service,
		Component: t.Component, Operation: t.Operation,
		Persistence: PersistenceNotRequested,
	}
}
