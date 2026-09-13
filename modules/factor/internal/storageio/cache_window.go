package storageio

import (
	"context"
	"fmt"
	"reflect"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fullCacheWindow struct {
	Subjects []string
	Columns  []inputcache.Column
	Rows     [][]any
}

// readFullCacheWindows bounds each full-column response by its known source
// schema, discovering it on demand when absent. Batches contain disjoint
// subjects, never fragments of one history.
// consume may observe valid earlier batches before a later error; it must not
// treat partial delivery as completion of the entire request.
func (c *Client) readFullCacheWindows(ctx context.Context, key WindowKey, span *pb.TimeRange, limit int, schema []inputcache.Column, consume func(fullCacheWindow) error) error {
	if pb.SeriesWindowSubjectLimit(limit, 0) == 0 || consume == nil {
		return nonRetryableRead(fmt.Errorf("full cache window requires a consumer and a schema within the cell budget"))
	}
	ids := uniqueSortedStrings(append(append([]string{}, key.SubjectIDs...), key.SubjectID))
	if len(ids) == 0 {
		return nonRetryableRead(fmt.Errorf("full cache window requires subjects"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(schema) == 0 {
		probe := key
		probe.SubjectID, probe.SubjectIDs = ids[0], nil
		window, err := c.readFullCacheWindow(ctx, probe, span, 1)
		if err != nil {
			return err
		}
		// Probe rows do not satisfy the requested lookback and are not delivered.
		schema = window.Columns
	}
	batchSize := pb.SeriesWindowSubjectLimit(limit, len(schema)-pb.SeriesWindowKeyColumns)
	if batchSize == 0 {
		return nonRetryableRead(fmt.Errorf("full cache schema exceeds single-subject cell budget"))
	}
	for start := 0; start < len(ids); start += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		part := key
		part.SubjectID = ""
		part.SubjectIDs = append([]string(nil), ids[start:min(start+batchSize, len(ids))]...)
		window, err := c.readFullCacheWindow(ctx, part, span, limit)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(schema, window.Columns) {
			return nonRetryableRead(fmt.Errorf("full cache response schema changed within its contract"))
		}
		if err := consume(window); err != nil {
			return err
		}
	}
	return nil
}

// readFullCacheWindow obtains schema and complete rows from one fenced source
// read. An empty result is valid, but is not a reusable coverage certificate.
func (c *Client) readFullCacheWindow(ctx context.Context, key WindowKey, span *pb.TimeRange, limit int) (fullCacheWindow, error) {
	rsp, _, err := c.querySeriesWindow(ctx, key, span, limit, nil)
	if err != nil {
		return fullCacheWindow{}, err
	}
	columns, rows, err := decodeCacheRows(rsp.GetColumns(), rsp.GetRows())
	if err != nil {
		return fullCacheWindow{}, nonRetryableRead(err)
	}
	return fullCacheWindow{Subjects: uniqueSortedStrings(append(append([]string{}, key.SubjectIDs...), key.SubjectID)), Columns: columns, Rows: rows}, nil
}
