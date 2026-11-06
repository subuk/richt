package engine

import (
	"context"
	"errors"
	"sync"

	"github.com/subuk/richt/facts"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/transport"
)

// hostFacts delays even cache identity validation until an evaluated fact call.
// Programmatic inventories can omit an identity when persistent caching is off.
func hostFacts(r *inventory.Resolved, h inventory.ResolvedHost, options DiffOptions) func(context.Context, string) (any, error) {
	var once sync.Once
	var provider *facts.Provider
	var initErr error
	return func(ctx context.Context, path string) (any, error) {
		if err := facts.ValidatePath(path); err != nil {
			return nil, err
		}
		once.Do(func() {
			settings := facts.Options{MaxAge: facts.DefaultMaxAge}
			if options.Facts != nil {
				settings = *options.Facts
			}
			if settings.CachedOnly {
				initErr = errors.New("cached-only facts are not supported during preparation")
				return
			}
			identity := facts.Identity{Inventory: r.SourceIdentity, Host: h.Name, Transport: h.Transport.Type,
				Target: h.Transport.Host, User: h.Transport.User, Port: h.Transport.Port, Sudo: h.Transport.Sudo}
			provider, initErr = facts.NewProvider(identity, settings, func(ctx context.Context) (facts.Snapshot, error) {
				if err := reportPreparation(ctx, options.Observer, PrepareEvent{Phase: "facts", Host: h.Name}); err != nil {
					return facts.Snapshot{}, err
				}
				var target transport.Transport
				var err error
				if options.FactTransport != nil {
					target, err = options.FactTransport(h.Transport)
				} else {
					target, err = transport.New(h.Transport)
				}
				if err != nil {
					return facts.Snapshot{}, err
				}
				if target == nil {
					return facts.Snapshot{}, errors.New("missing fact transport")
				}
				return target.Facts(ctx)
			})
		})
		if initErr != nil {
			return nil, initErr
		}
		return provider.Lookup(ctx, path)
	}
}
