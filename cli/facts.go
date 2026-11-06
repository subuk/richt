package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/subuk/richt/facts"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/transport"
)

// Pointers preserve explicit flags, including values equal to the defaults,
// because saved-plan execution must reject every preparation option.
type preparationFacts struct {
	FactsCacheDir *string        `help:"Controller facts cache directory." type:"path"`
	FactsMaxAge   *time.Duration `help:"Maximum facts cache age (default 72h; zero forces collection)."`
	RefreshFacts  *bool          `help:"Collect fresh facts and replace successful cached snapshots."`
	NoFactsCache  *bool          `help:"Disable persistent facts cache reads and writes."`
}

func (f preparationFacts) specified() bool {
	return f.FactsCacheDir != nil || f.FactsMaxAge != nil || f.RefreshFacts != nil || f.NoFactsCache != nil
}
func (f preparationFacts) options() (facts.Options, error) {
	o := facts.Options{MaxAge: facts.DefaultMaxAge}
	if f.FactsCacheDir != nil {
		o.CacheDir = *f.FactsCacheDir
	}
	if f.FactsMaxAge != nil {
		o.MaxAge = *f.FactsMaxAge
	}
	if f.RefreshFacts != nil {
		o.Refresh = *f.RefreshFacts
	}
	if f.NoFactsCache != nil {
		o.NoCache = *f.NoFactsCache
	}
	return o, o.Validate()
}

type factsSelection struct {
	Inventory       string   `short:"i" help:"YAML inventory path or HTTPS URL."`
	SourceConfig    string   `help:"Explicit local YAML settings for HTTPS authentication, CA and secret bindings." type:"path"`
	InventorySHA256 string   `name:"inventory-sha256" help:"Expected SHA-256 of an HTTPS inventory."`
	Hosts           []string `name:"host" help:"Select host names (repeat or comma separate)."`
	Tags            []string `name:"tag" help:"Select hosts carrying these tags."`
}
type factsArguments struct {
	factsSelection
	FactsCacheDir string        `help:"Controller facts cache directory." type:"path"`
	FactsMaxAge   time.Duration `default:"72h" help:"Maximum facts cache age; zero forces collection."`
	Refresh       bool          `help:"Collect fresh facts and replace successful cached snapshots."`
	Cached        bool          `help:"Inspect cached facts, including stale snapshots, without contacting targets."`
	NoFactsCache  bool          `help:"Disable persistent facts cache reads and writes."`
	Parallelism   int           `default:"20" help:"Maximum number of hosts to collect concurrently (positive)."`
	Progress      string        `default:"auto" enum:"auto,plain,off" help:"Progress on stderr: auto, plain, or off."`
}

type factsOutput struct {
	SchemaVersion int               `json:"schema_version"`
	Hosts         []factsHostResult `json:"hosts"`
}
type factsHostResult struct {
	Host string `json:"host"`
	*facts.Snapshot
	Source     string  `json:"source,omitempty"`
	AgeSeconds float64 `json:"age_seconds"`
	Stale      bool    `json:"stale"`
	Error      string  `json:"error,omitempty"`
}
type hostFactsCollector func(context.Context, inventory.Transport) (facts.Snapshot, error)

func collectHostFacts(ctx context.Context, connection inventory.Transport) (facts.Snapshot, error) {
	client, err := transport.New(connection)
	if err != nil {
		return facts.Snapshot{}, err
	}
	return client.Facts(ctx)
}

func executeFacts(ctx context.Context, f factsArguments, out, stderr io.Writer, collect hostFactsCollector) error {
	if f.Parallelism <= 0 {
		return fmt.Errorf("parallelism must be positive")
	}
	options := facts.Options{CacheDir: f.FactsCacheDir, MaxAge: f.FactsMaxAge, Refresh: f.Refresh, CachedOnly: f.Cached, NoCache: f.NoFactsCache}
	if err := options.Validate(); err != nil {
		return err
	}
	selected := selection{Inventory: f.Inventory, SourceConfig: f.SourceConfig, InventorySHA256: f.InventorySHA256, Hosts: f.Hosts, Tags: f.Tags}
	resolved, _, err := selected.load(ctx)
	if err != nil {
		return err
	}
	results := factsOutput{SchemaVersion: 1, Hosts: make([]factsHostResult, len(resolved.Hosts))}
	progress := newProgress(stderr, f.Progress, "Collecting facts")
	progress.mu.Lock()
	progress.total = len(resolved.Hosts)
	progress.mu.Unlock()
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(f.Parallelism, len(resolved.Hosts)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				host := resolved.Hosts[i]
				progress.factHost(host.Name, true, false)
				result := factsHostResult{Host: host.Name}
				connection := host.Transport
				identity := facts.Identity{Inventory: resolved.SourceIdentity, Host: host.Name, Transport: connection.Type, Target: connection.Host, User: connection.User, Port: connection.Port, Sudo: connection.Sudo}
				provider, hostErr := facts.NewProvider(identity, options, func(callCtx context.Context) (facts.Snapshot, error) { return collect(callCtx, connection) })
				if hostErr == nil {
					var value facts.Result
					value, hostErr = provider.Get(ctx)
					if hostErr == nil {
						result.Snapshot = &value.Snapshot
						result.Source, result.AgeSeconds, result.Stale = value.Source, value.Age.Seconds(), value.Stale
					}
				}
				if hostErr != nil {
					result.Error = hostErr.Error()
				}
				results.Hosts[i] = result
				progress.factHost(host.Name, false, hostErr != nil)
			}
		}()
	}
	for i := range resolved.Hosts {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	progressErr := progress.stop()
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		return err
	}
	failed := 0
	for _, result := range results.Hosts {
		if result.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("facts failed for %d host(s); see JSON results", failed)
	}
	return progressErr
}
