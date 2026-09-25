package marketwiring

import (
	"context"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type timerHandlerStorage struct{}

func (timerHandlerStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}
