// Package trpcretry provides bounded retry policies for idempotent tRPC calls.
package trpcretry

import (
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-filter/slime/retry"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

var readOnlyFilter = mustReadOnlyFilter()

// ReadOnly returns a bounded retry filter for explicitly reviewed read calls.
func ReadOnly() filter.ClientFilter {
	return readOnlyFilter
}

func mustReadOnlyFilter() filter.ClientFilter {
	r, err := retry.New(2, []int{int(errs.RetClientNetErr), int(errs.RetClientTimeout)}, retry.WithLinearBackoff(20*time.Millisecond))
	if err != nil {
		panic(fmt.Sprintf("build read-only retry filter: %v", err))
	}
	return r.Invoke
}
