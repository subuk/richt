package engine

import "context"

// PrepareEvent identifies the work about to run during plan preparation.
// Phase is rendering, checking, or observing. An empty Service identifies
// host-wide work. Events contain labels only, without prepared inputs or errors.
type PrepareEvent struct {
	Phase   string
	Host    string
	Service string
}

// DiffOptions configures preparation reporting without changing the plan.
type DiffOptions struct {
	// Observer is called synchronously before the reported work begins.
	// It must return promptly. The event is a detached value safe to retain.
	Observer func(PrepareEvent)
}

func reportPreparation(ctx context.Context, observer func(PrepareEvent), event PrepareEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if observer != nil {
		observer(event)
	}
	return ctx.Err()
}
