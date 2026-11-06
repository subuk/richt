package plan_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subuk/richt/plan"
)

func eventKinds(events []plan.Event) []plan.EventKind {
	kinds := make([]plan.EventKind, len(events))
	for i, event := range events {
		kinds[i] = event.Kind
	}
	return kinds
}

func TestApplyEventsContinuationFailureAndMetadata(t *testing.T) {
	p := sample()
	p.Hosts[0].Tasks[0].Progress.Status = "success"
	p.Hosts[0].Tasks[1].Service = "web"
	p.Hosts[0].Tasks[1].Component = "process"
	p.Hosts[0].Tasks[1].Operation = "restart"
	var events []plan.Event
	_, err := plan.Apply(context.Background(), p, &executor{fail: "two"}, nil, plan.ApplyOptions{
		HostConcurrency: 1, Observer: func(event plan.Event) { events = append(events, event) },
	})
	if err == nil {
		t.Fatal("expected execution failure")
	}
	want := []plan.EventKind{plan.EventApplyStarted, plan.EventTaskSkipped, plan.EventTaskStarted,
		plan.EventTaskFailed, plan.EventTaskBlocked, plan.EventTaskStarted, plan.EventTaskSucceeded, plan.EventApplyFinished}
	if !reflect.DeepEqual(eventKinds(events), want) {
		t.Fatalf("events %v, want %v", eventKinds(events), want)
	}
	for i, event := range events {
		if event.Time.IsZero() || i > 0 && event.Time.Before(events[i-1].Time) {
			t.Fatalf("event timestamps are not ordered: %+v", events)
		}
		if event.Persistence != plan.PersistenceNotRequested {
			t.Fatalf("unexpected persistence %+v", event)
		}
	}
	started, failed, blocked := events[2], events[3], events[4]
	if started.Host != "first" || started.TaskID != "two" || started.Service != "web" || started.Component != "process" || started.Operation != "restart" {
		t.Fatalf("missing task identity: %+v", started)
	}
	if failed.Error != "planned failure" || blocked.Error != failed.Error {
		t.Fatalf("failure and blocked reason: %+v %+v", failed, blocked)
	}
	if events[0].Total != 4 || events[len(events)-1].Status != "error" || events[len(events)-1].Error == "" {
		t.Fatalf("incorrect lifecycle snapshots: %+v", events)
	}
	// Retaining or changing a received scalar snapshot cannot change the plan.
	events[2].TaskID = "observer changed this"
	if p.Hosts[0].Tasks[1].ID != "two" {
		t.Fatal("observer mutated plan")
	}
}

func TestApplyEventsStartBeforePreflightAndFinishOnFailure(t *testing.T) {
	p := manyHosts(1)
	var events []plan.Event
	e := hostExecutor{resolve: func(_ string, _ plan.Task) ([]plan.Action, error) {
		if len(events) != 1 || events[0].Kind != plan.EventApplyStarted {
			t.Error("preflight ran before start event")
		}
		return nil, errors.New("invalid prepared operation")
	}}
	_, err := plan.Apply(context.Background(), p, e, nil, plan.ApplyOptions{
		HostConcurrency: 1, Observer: func(event plan.Event) { events = append(events, event) },
	})
	if err == nil || !reflect.DeepEqual(eventKinds(events), []plan.EventKind{plan.EventApplyStarted, plan.EventApplyFinished}) || events[1].Status != "error" {
		t.Fatalf("error %v, events %+v", err, events)
	}
	events = nil
	_, err = plan.Apply(context.Background(), nil, e, nil, plan.ApplyOptions{
		HostConcurrency: 1, Observer: func(event plan.Event) { events = append(events, event) },
	})
	if err == nil || !reflect.DeepEqual(eventKinds(events), []plan.EventKind{plan.EventApplyStarted, plan.EventApplyFinished}) || events[1].Status != "error" {
		t.Fatalf("nil plan error %v, events %+v", err, events)
	}
}

func TestApplyEventsPersistenceAndExecutionAreIndependent(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			p := manyHosts(1)
			var events []plan.Event
			calls := 0
			_, err := plan.Apply(context.Background(), p, &executor{}, func(*plan.Plan) error {
				calls++
				if calls == failAt {
					return errors.New("synthetic-secret-in-save-error")
				}
				return nil
			}, plan.ApplyOptions{HostConcurrency: 1, Observer: func(event plan.Event) { events = append(events, event) }})
			if err == nil {
				t.Fatal("expected save failure")
			}
			var succeeded, saveFailures, blocked int
			for _, event := range events {
				if strings.Contains(event.Error, "synthetic-secret") {
					t.Fatal("persistence error escaped through event")
				}
				switch event.Kind {
				case plan.EventTaskSucceeded:
					succeeded++
					if event.Persistence != plan.PersistenceFailed || p.Hosts[0].Tasks[0].Progress.Status != "success" {
						t.Fatalf("lost execution success: %+v", event)
					}
				case plan.EventPersistenceFailed:
					saveFailures++
					if event.Persistence != plan.PersistenceFailed {
						t.Fatalf("incorrect persistence failure: %+v", event)
					}
				case plan.EventTaskBlocked:
					blocked++
				}
			}
			if succeeded != failAt-1 || saveFailures != 1 || blocked != 3-failAt {
				t.Fatalf("incorrect events: %+v", events)
			}
			final := events[len(events)-1]
			if final.Kind != plan.EventApplyFinished || final.Status != "error" || final.Persistence != plan.PersistenceFailed {
				t.Fatalf("incorrect final event: %+v", final)
			}
		})
	}
}

func TestApplyEventsSensitiveErrors(t *testing.T) {
	marker := "synthetic-sensitive-event-marker\nsecond line"
	for _, preflight := range []bool{false, true} {
		p := manyHosts(1)
		p.Hosts[0].Tasks[0].Operation = "file"
		p.Hosts[0].Tasks[0].File = &plan.FileChange{Path: "/example", Sensitive: true, Content: marker, Patch: marker}
		prepareFileFixture(p.Hosts[0].Tasks[0].File)
		var events []plan.Event
		e := hostExecutor{
			resolve: func(_ string, task plan.Task) ([]plan.Action, error) {
				if preflight {
					return nil, errors.New(marker)
				}
				return []plan.Action{{Kind: task.ID}}, nil
			},
			run: func(context.Context, string, string) error { return errors.New(marker) },
		}
		_, err := plan.Apply(context.Background(), p, e, nil, plan.ApplyOptions{
			HostConcurrency: 1, Observer: func(event plan.Event) { events = append(events, event) },
		})
		if err == nil {
			t.Fatal("expected sensitive failure")
		}
		data, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "synthetic-sensitive") || strings.Contains(string(data), "second line") {
			t.Fatalf("sensitive events exposed prepared content: %s", data)
		}
	}
}

func TestApplyEventsCancellation(t *testing.T) {
	p := manyHosts(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []plan.Event
	e := hostExecutor{run: func(ctx context.Context, _, _ string) error {
		cancel()
		return ctx.Err()
	}}
	_, err := plan.Apply(ctx, p, e, nil, plan.ApplyOptions{
		HostConcurrency: 1, Observer: func(event plan.Event) { events = append(events, event) },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	canceled := 0
	for _, event := range events {
		if event.Kind == plan.EventApplyCanceled {
			canceled++
		}
	}
	if canceled != 1 || events[len(events)-1].Status != "canceled" {
		t.Fatalf("incorrect cancellation events: %+v", events)
	}
}

func TestApplyEventsCancellationReportedWhileAwaitingActiveAction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 1)
	canceled := make(chan string, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	e := hostExecutor{run: func(context.Context, string, string) error {
		started <- "started"
		<-release
		return nil
	}}
	go func() {
		_, err := plan.Apply(ctx, manyHosts(1), e, nil, plan.ApplyOptions{HostConcurrency: 1, Observer: func(event plan.Event) {
			if event.Kind == plan.EventApplyCanceled {
				canceled <- "canceled"
			}
		}})
		done <- err
	}()
	receive(t, started)
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("cancellation was not reported while awaiting an active action")
	}
	select {
	case err := <-done:
		t.Fatalf("apply did not await active action: %v", err)
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error %v", err)
	}
}

func TestApplySlowObserverAllowsConcurrentProgressPersistence(t *testing.T) {
	p := manyHosts(2)
	for i := range p.Hosts {
		p.Hosts[i].Tasks = p.Hosts[i].Tasks[:1]
	}
	bStarted := make(chan struct{})
	releaseB := make(chan struct{})
	bSaved := make(chan struct{})
	var inObserver atomic.Int32
	var events []plan.Event
	e := hostExecutor{run: func(_ context.Context, host, _ string) error {
		if host == "host00" {
			<-bStarted
		} else {
			close(bStarted)
			<-releaseB
		}
		return nil
	}}
	_, err := plan.Apply(context.Background(), p, e, func(snapshot *plan.Plan) error {
		if snapshot.Hosts[1].Tasks[0].Progress.Status == "success" {
			close(bSaved)
		}
		return nil
	}, plan.ApplyOptions{HostConcurrency: 2, Observer: func(event plan.Event) {
		if inObserver.Add(1) != 1 {
			t.Error("concurrent observer callbacks")
		}
		defer inObserver.Add(-1)
		events = append(events, event)
		if event.Kind == plan.EventTaskSucceeded && event.Host == "host00" {
			close(releaseB)
			select {
			case <-bSaved:
			case <-time.After(5 * time.Second):
				t.Error("observer held the progress lock")
			}
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"host00", "host01"} {
		var kinds []plan.EventKind
		for _, event := range events {
			if event.Host == host {
				kinds = append(kinds, event.Kind)
			}
		}
		if !reflect.DeepEqual(kinds, []plan.EventKind{plan.EventTaskStarted, plan.EventTaskSucceeded}) {
			t.Fatalf("host %s events %v", host, kinds)
		}
	}
	final := events[len(events)-1]
	if final.Status != "success" || final.Persistence != plan.PersistenceSaved {
		t.Fatalf("incorrect final event: %+v", final)
	}
}
