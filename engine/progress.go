package engine

import (
	"context"

	"github.com/subuk/richt/source"
)

// PrepareEvent identifies the work about to run during plan preparation.
// Phase is rendering, checking, or observing. An empty Service identifies
// host-wide work. Events contain labels only, without prepared inputs or errors.
type PrepareEvent struct {
	Phase   string
	Host    string
	Service string
}

// DiffOptions configures preparation inputs and reporting without changing the plan.
type DiffOptions struct {
	// Sources supplies the controller-side HTTPS template reader. A nil value
	// creates one shared default client when the first remote template is needed.
	Sources *source.Client

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
