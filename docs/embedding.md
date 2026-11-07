---
title: "Embedding in Go"
description: "Prepare and apply plans through public Go packages with structured progress and cancellation."
weight: 85
---

Richt's CLI consumes the same public packages available to an embedded caller.
There is no daemon, additional SDK, or required terminal framework. The
[compiled example](https://github.com/subuk/richt/blob/main/examples/embedding/embedding_test.go)
constructs an inventory in memory, resolves it, prepares and displays a plan,
saves/opens it, and attaches an observer when applying it. It deliberately does
not execute against a host during `go test`.

## Entry points and requirements

| Package | Entry points and responsibility |
| --- | --- |
| `inventory` | Construct `Inventory`; `Resolve(Selectors)` validates and resolves assignments without host or secret access. |
| `inventoryyaml` | `Load` and `LoadWithOptions` adapt local or HTTPS YAML into inventory. |
| `source` | `New(Options)` creates a bounded authenticated HTTPS client; `Read(ctx, url, sha256)` returns a byte snapshot and final response URL. |
| `facts` | Typed `Snapshot`, strict `Lookup`, cache `Options`/`Identity`, and lazy `NewProvider`/`Get`. |
| `transport` | `Transport.Facts(ctx)` observes a Linux target; `New(inventory.Transport)` creates the local/SSH implementation without contacting it. |
| `engine` | `Local.Diff(ctx, resolved, ...DiffOptions)` prepares executable inputs. `Local` also dispatches execution through the selected local/SSH backends. |
| `plan` | Inspect `Plan`, use `Display` for safe human-readable review, `Save`/`Open` for protected persistence and locking, and `Apply` for execution. |
| `cli` | `RunContext` accepts a caller-owned context; `Run` retains the background-context entry point. Applications using domain packages supply their own confirmation and UI. |

Import from `github.com/subuk/richt`. The current module requires **Go 1.27.3**,
as declared in `go.mod`; this feature does not raise that requirement. Pin a
reviewed revision/version and check the consuming application's toolchain
explicitly. Vmango toolchain alignment is a separate integration decision.
Controller and Linux target requirements remain those in
[installation](installation.md) and the selected resource references.

## Apply events

Pass an `Observer func(plan.Event)` in `plan.ApplyOptions`, together with a
positive `HostConcurrency` (normally `plan.DefaultHostConcurrency`). Omitting
options preserves the existing default concurrency. Observation is optional
and independent of the persistence callback:

```go
result, err := plan.Apply(ctx, store.Plan, engine.Local{}, store.Save,
    plan.ApplyOptions{
        HostConcurrency: plan.DefaultHostConcurrency,
        Observer: func(e plan.Event) {
            // Copy into application-owned UI state; do not inspect store.Plan.
            fmt.Printf("%s %s %s\n", e.Kind, e.Host, e.TaskID)
        },
    })
```

`Event` holds value fields: kind, time, host, task ID, service, component,
operation, safe error, total task count on apply lifecycle events, and persistence
state. Task identity is
the `(Host, TaskID)` pair; service/component labels are for display. The IDs
remain fixed when continuing a saved plan. Counts include previously completed
tasks and make no claim about progress inside an opaque operation.

The lifecycle includes `apply_started`, `task_started`, `task_succeeded`,
`task_failed`, `task_skipped`, `task_blocked`, `apply_canceled`,
`persistence_failed` and `apply_finished`. The final event has status `success`,
`error` or `canceled`; a nonempty safe error denotes failure. Always inspect
the returned error and result as well. Preflight failures finish the invocation
without task execution.

Delivery rules:

- Callbacks are serialized within one `Apply` call and are synchronous. Events
  for a host retain task order; different hosts can interleave. Concurrent
  independent `Apply` calls do not share a callback lock.
- UI callbacks never run under the plan progress/persistence lock. A slow
  callback delays delivery and the workers awaiting it, so callbacks should
  return promptly. There is no unbounded event queue or background event
  history. Callback panics are not recovered; callbacks must not panic.
- Events contain detached values, without mutable plan pointers, payloads,
  command arguments, file contents or subprocess output. They can be copied
  into another goroutine's state using the caller's own synchronization.
  The caller must not read or mutate the executing plan concurrently.
- Callback completion precedes `Apply` returning. Cancellation does not
  interrupt arbitrary callback code; a callback must not wait for `Apply`
  itself to finish.

Execution and persistence have separate outcomes. `task_succeeded` means the
operation completed and was verified. Its `Persistence` can still be `failed`
if saving that result failed. `not_requested` means no persistence write is
associated with that event (including when no callback was supplied); `saved`
describes a successful persistence boundary.
A persistence error emits its own event, stops new actions globally and makes
the final invocation fail, even if later saves succeed. Already active actions
are awaited and their final status is recorded. The observer is never used to
save progress or decide which tasks to retry.

Built-in errors follow the existing sensitive-output rules. Persistence callback
errors are generic in events because an arbitrary callback could include plan
contents in its error. Custom executors and the caller's own logging must also
respect those rules; events cannot sanitize secrets intentionally embedded in
user-defined display labels.

## Preparation and cancellation

`engine.DiffOptions.Observer` receives `PrepareEvent` values with `Phase`,
`Host` and `Service`. Phases are `rendering`, `facts`, `checking` and `observing`; an
empty service identifies host-wide checks. Calls are synchronous and precede
the corresponding work. These are activity labels, not task completions or
estimated percentages. Template preparation precedes resource observation. An
evaluated `fact` call can collect target observations while rendering; templates
without evaluated fact calls retain the existing ordering. Inventory inspection can fetch inventory/service documents, never
templates or secret values, and has no progress subscription.

## HTTPS source clients

Create one `source.Client` per preparation and pass it through loading and diff
to share downloaded byte snapshots, limits, and scoped authentication:

```go
sources, err := source.New(source.Options{CAFile: "/path/to/extra-ca.pem"})
if err != nil {
    return err
}
in, err := inventoryyaml.LoadWithOptions(ctx, inventoryURL, inventoryyaml.LoadOptions{
    Sources: sources,
    SHA256: inventoryDigest,
    SecretSources: map[string]inventory.SecretSource{
        "production": {Type: "yaml", File: "/private/secrets.yaml"},
        "ci": {Type: "env", Env: map[string]string{"database.password": "APP_DB_PASSWORD"}},
    },
})
if err != nil {
    return err
}
resolved, err := in.Resolve(inventory.Selectors{})
if err != nil {
    return err
}
prepared, err := (engine.Local{}).Diff(ctx, resolved, engine.DiffOptions{Sources: sources})
```

`source.Options` also accepts `Auth []source.Auth` and configurable `Timeout`,
`MaxBytes`, `MaxTotalBytes`, `MaxDocuments`, and `MaxRedirects`. Authentication
entries contain `Origin`, `PathPrefix`, and either `BearerToken` or
`Username`/`Password`. Embedded callers own credential acquisition; the CLI's
env/file reference format belongs to `--source-config`. Never log credential
values. `inventoryyaml.LoadOptions.SecretSources` accepts YAML bindings with
absolute local paths or environment bindings with explicit `Env` key-to-variable
maps. Both reject collisions with inventory declarations. Environment values
are read lazily during preparation, never during loading or saved-plan apply.

`config.PrepareContext(ctx, config, state, vars, sources, lookups...)` acquires
then renders one template. Existing `config.Prepare` uses a background context
and a nil source client. Nil clients create a default only when remote template
bytes are needed; `engine.Local.Diff` shares its default within the diff. The
client caches bytes, while each host renders with its own variables and secret
lookup. Context cancellation reaches HTTPS requests. See [HTTPS sources](sources.md)
for validation, redirect rules, defaults and saved-plan boundaries.

## Host facts

`engine.DiffOptions.Facts` is an optional `*facts.Options`. Nil selects the
72-hour cache default; an explicit options value keeps its zero `MaxAge`, which
forces collection. `NoCache` disables persistent reads/writes, `Refresh` bypasses
reads, and `CacheDir` isolates storage. `CachedOnly` is for inspection and is
rejected during preparation. Providers initialize only on the first evaluated
fact lookup and are shared by all host templates, including units/drop-ins.

```go
prepared, err := (engine.Local{}).Diff(ctx, resolved, engine.DiffOptions{
    Facts: &facts.Options{
        CacheDir: "/private/app-cache/facts",
        MaxAge: facts.DefaultMaxAge,
    },
})
```

Loaded and resolved inventories retain `SourceIdentity` as non-serialized
metadata: the configured absolute local root path or requested HTTPS root URL.
The cache namespaces by that source and explicit host/transport inputs. For an
inventory constructed in memory, set a stable `SourceIdentity` yourself or use
`Facts: &facts.Options{NoCache: true}`. An inventory without evaluated fact calls
needs no identity and never accesses the cache. Source identity is configured
identity, without resolving DNS/SSH aliases or HTTP redirect destinations;
retargeting requires explicit refresh.

`engine.DiffOptions.FactTransport` accepts
`func(inventory.Transport) (transport.Transport, error)` for an embedded
transport implementation or test double. The public `transport.Transport`
interface includes `Run`, `RunInput`, `RunDiscard`, and
`Facts(context.Context) (facts.Snapshot, error)`. Its
constructor is lazy; the engine calls it only when collection is necessary,
so cache hits require no target contact. Custom collectors must return complete,
validated snapshots and honor cancellation; the built-in collector has a
30-second host timeout. `facts.NewProvider(identity, options, collect)` wraps a
collection callback, and `Get(ctx)` returns a detached `facts.Result` containing
`Snapshot`, `Source`, `Age`, and `Stale`. `Lookup(ctx, path)` validates schema
paths before obtaining the immutable per-invocation snapshot.

For direct file-template preparation, use
`config.PrepareWithOptions(ctx, config, state, vars, config.PrepareOptions{...})`.
Options contain `Sources *source.Client`, `Secrets config.SecretLookup`, and
`Fact func(context.Context, string) (any, error)`. The fact callback receives a
schema path and returns its typed value. Callers composing multiple templates
must share a host-scoped provider and keep values isolated between hosts.
The older `Prepare` and `PrepareContext` entry points remain available.
Absent assignments do not acquire templates or invoke either lookup.

Facts are rendered preparation inputs only. Saved-plan apply and continuation
have no dependency on the facts cache or collector and do not persist a full
snapshot. See [host facts](facts.md) for schema, target requirements, cache
failure policy, and privacy boundaries.

## Caller cancellation

The caller owns the context. Connect an application's Cancel button to
`context.WithCancel`; a terminal executable can use `signal.NotifyContext`.
Canceled execution stops admitting new actions and waits for active work to
return. Canceling a transport client does not guarantee that an already accepted
remote command or systemd job stopped. Check the result and saved progress.

To resume, close the previous store, obtain a fresh context, reopen the same
saved plan and call `Apply` again. Previously successful tasks emit skips;
unfinished tasks run in the original order. No re-rendering, inventory reload,
automatic retry or rollback is introduced. With no persistence callback, progress
exists only in memory. See [saved plans](files.md) and
[continuation](continuation.md) for locking, security and interruption limits.
