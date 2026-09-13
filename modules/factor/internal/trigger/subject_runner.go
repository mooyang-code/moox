package trigger

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
)

type SubjectCatalog interface {
	CatalogSnapshot(context.Context) (*domain.CatalogSnapshot, error)
}

type SubjectTaskLedger interface {
	AdmitSubjectTask(context.Context, engine.FactorTask) (bool, error)
	CompleteSubjectTask(context.Context, engine.FactorTask, string) error
}

// SubjectRunCommit must persist the source events and task outcomes before
// success can release the delivery. It runs under the catalog operation gate.
type SubjectRunCommit func(context.Context, SubjectBatchReceipt) error

// SubjectBatchReceipt retains every planned task, including tasks skipped by
// durable admission. A receipt writer must resolve those from the ledger.
type SubjectBatchReceipt struct {
	CatalogRevision int64
	Events          []SubjectEvent
	Tasks           []taskrunner.Task
	Results         []taskrunner.Result
}

type SubjectRunner struct {
	catalog    SubjectCatalog
	ledger     SubjectTaskLedger
	runner     CombinationTaskRunner
	gate       *taskrunner.OperationGate
	commit     SubjectRunCommit
	factorsDir string
}

func NewSubjectRunner(catalog SubjectCatalog, ledger SubjectTaskLedger, runner CombinationTaskRunner, gate *taskrunner.OperationGate, commit SubjectRunCommit, factorsDir string) (*SubjectRunner, error) {
	if catalog == nil || ledger == nil || runner == nil || gate == nil || commit == nil {
		return nil, fmt.Errorf("subject runner requires catalog, task runner, shared gate and durable commit")
	}
	return &SubjectRunner{catalog: catalog, ledger: ledger, runner: runner, gate: gate, commit: commit, factorsDir: factorsDir}, nil
}

func (r *SubjectRunner) Execute(ctx context.Context, events []SubjectEvent) error {
	if len(events) == 0 {
		return nil
	}
	release, err := r.gate.AcquireContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, err := r.catalog.CatalogSnapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return fmt.Errorf("activated catalog is missing")
	}
	var tasks []taskrunner.Task
	expected := map[string]taskrunner.Task{}
	for _, event := range events {
		built, err := BuildSubjectTasks(*snapshot, event, r.factorsDir)
		if err != nil {
			return err
		}
		for _, task := range built {
			if pinned, duplicate := expected[task.TaskID]; duplicate {
				if pinned.SourceNodeID != task.SourceNodeID || pinned.SourceStoreID != task.SourceStoreID || pinned.SourceSequence != task.SourceSequence || pinned.SourceEventID != task.SourceEventID {
					return fmt.Errorf("subject delivery identity was reused with different provenance")
				}
				continue
			}
			expected[task.TaskID] = task
			tasks = append(tasks, task)
		}
	}
	// Admit newest revisions first, so an older delivery in the same microbatch
	// cannot write after the newer one. Store identities remain incomparable in
	// the ledger; this ordering does not imply ordering between source stores.
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].SourceSequence > tasks[j].SourceSequence })
	planned := append([]taskrunner.Task(nil), tasks...)
	admitted := make([]taskrunner.Task, 0, len(tasks))
	for _, task := range tasks {
		run, err := r.ledger.AdmitSubjectTask(ctx, task.FactorTask)
		if err != nil {
			return err
		}
		if run {
			admitted = append(admitted, task)
		} else {
			delete(expected, task.TaskID)
		}
	}
	tasks = admitted
	var results []taskrunner.Result
	if len(tasks) > 0 {
		results = r.runner.RunAll(ctx, tasks)
	}
	seen := make(map[string]bool, len(results))
	var failures []error
	for i := range results {
		result := &results[i]
		task, found := expected[result.Task.TaskID]
		if !found || seen[result.Task.TaskID] {
			return fmt.Errorf("subject task runner returned unexpected or duplicate task %q", result.Task.TaskID)
		}
		seen[task.TaskID] = true
		// Persistence uses the pinned task, not metadata echoed by a worker.
		result.Task = task
		if result.Err != nil {
			failures = append(failures, result.Err)
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("subject task runner omitted task results")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, result := range results {
		failure := ""
		if result.Err != nil {
			failure = result.Err.Error()
			if failure == "" {
				failure = "task execution failed"
			}
		}
		if err := r.ledger.CompleteSubjectTask(ctx, result.Task.FactorTask, failure); err != nil {
			return err
		}
	}
	if err := r.commit(ctx, SubjectBatchReceipt{CatalogRevision: snapshot.Revision, Events: events, Tasks: planned, Results: results}); err != nil {
		return err
	}
	return errors.Join(failures...)
}
