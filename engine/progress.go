package engine

import (
	"context"

	"github.com/subuk/richt/facts"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/source"
	"github.com/subuk/richt/transport"
)

// PrepareEvent identifies the work about to run during plan preparation.
// Phase is rendering, facts, checking, or observing. An empty Service identifies
// host-wide work. Events contain labels only, without prepared inputs or errors.
type PrepareEvent struct {
	Phase   string
	Host    string
	Service string
}

// DiffOptions configures preparation inputs and reporting without changing the plan.
type DiffOptions struct {
	// Facts configures the lazy per-host snapshot. Nil uses the persistent cache
	// with its default lifetime. No fact cache or transport is used until a
	// template evaluates fact. CachedOnly is reserved for the facts command.
	Facts *facts.Options

	// FactTransport optionally supplies the transport used to collect facts.
	// Nil uses the standard local/SSH client. Binding is lazy, including on cache
	// hits, and each host calls Facts at most once during this preparation.
	FactTransport func(inventory.Transport) (transport.Transport, error)

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
