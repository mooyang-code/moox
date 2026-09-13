package trigger

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

type SubjectReceiptWriter interface {
	CommitSubjectReceipt(context.Context, store.SubjectReceiptInput) error
}

func NewSubjectReceiptCommit(writer SubjectReceiptWriter) (SubjectRunCommit, error) {
	if writer == nil {
		return nil, fmt.Errorf("subject receipt writer is required")
	}
	return func(ctx context.Context, batch SubjectBatchReceipt) error {
		type eventKey struct{ space, event string }
		planned := map[eventKey][]engine.FactorTask{}
		for _, event := range batch.Events {
			planned[eventKey{event.SpaceID, event.EventID}] = nil
		}
		for _, task := range batch.Tasks {
			key := eventKey{task.SpaceID, task.TriggerEventID}
			if _, found := planned[key]; !found {
				return fmt.Errorf("subject receipt contains a task without its event")
			}
			planned[key] = append(planned[key], task.FactorTask)
		}
		for _, event := range batch.Events {
			if err := writer.CommitSubjectReceipt(ctx, store.SubjectReceiptInput{SpaceID: event.SpaceID, EventID: event.EventID, CatalogRevision: batch.CatalogRevision, Source: event.Ready, Tasks: planned[eventKey{event.SpaceID, event.EventID}]}); err != nil {
				return err
			}
		}
		return nil
	}, nil
}
