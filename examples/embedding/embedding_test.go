// Package embedding demonstrates the existing public Richt packages.
package embedding

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/subuk/richt/engine"
	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/plan"
)

// This example is compiled by go test but is not executed: callers must choose
// their own target and plan paths and obtain agreement before calling apply.
func Example() {
	prepare := func(ctx context.Context, targetPath, planPath string, review io.Writer) error {
		order := 10
		content := "Hello from an embedded Richt application\n"
		in := inventory.Inventory{
			Tags: map[string]inventory.Tag{
				"app": {Services: map[string]inventory.ServiceAssignment{"greeting": {}}},
			},
			Services: map[string]inventory.ServiceDefinition{
				"greeting": {Order: &order, Service: inventory.Service{
					Configs: []inventory.Config{{
						Name: "message", Path: targetPath, Mode: "0600",
						Template: inventory.Template{Inline: &content},
					}},
				}},
			},
			Hosts: []inventory.Host{{Name: "local", Tags: []string{"app"},
				Transport: inventory.Transport{Type: "local"}}},
		}
		resolved, err := in.Resolve(inventory.Selectors{})
		if err != nil {
			return err
		}
		p, err := (engine.Local{}).Diff(ctx, resolved)
		if err != nil {
			return err
		}
		// Inspect p.Hosts and their tasks directly, or use the safe text display.
		if err := plan.Display(review, p, engine.Local{}); err != nil {
			return err
		}
		return plan.Save(planPath, p)
	}

	apply := func(ctx context.Context, planPath string, observe func(plan.Event)) (plan.Result, error) {
		store, err := plan.Open(planPath)
		if err != nil {
			return plan.Result{}, err
		}
		defer store.Close()
		result, err := plan.Apply(ctx, store.Plan, engine.Local{}, store.Save,
			plan.ApplyOptions{HostConcurrency: plan.DefaultHostConcurrency, Observer: observe})
		result.PlanPath = planPath
		return result, err
	}

	// Application UI handlers can call cancel() while apply is running. Call
	// apply again with a fresh context to resume after fixing the external cause;
	// do not call prepare again, which would replace the executable saved inputs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observe := func(e plan.Event) {
		fmt.Printf("%s %s %s %s persistence=%s\n",
			e.Kind, e.Host, e.TaskID, e.Error, e.Persistence)
	}
	// These are example paths on a Linux host. In a real application the caller
	// chooses them and gates the apply call on the user's review/approval.
	const planPath = "/var/tmp/richt-embedded-plan.json"
	if err := prepare(ctx, "/var/tmp/richt-embedded-message", planPath, os.Stdout); err != nil {
		fmt.Println(err)
		return
	}
	result, err := apply(ctx, planPath, observe)
	if err != nil {
		fmt.Println(err)
	}
	if err := plan.DisplayResult(os.Stdout, result); err != nil {
		fmt.Println(err)
	}
}
