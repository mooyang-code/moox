package access

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
)

type DerivedErrorReporter func(ctx context.Context, stage string, err error)

type Options struct {
	Root               string
	Metadata           metadata.Store
	MetadataPath       string
	InitSchemaPath     string
	PebblePath         string
	DuckDBPath         string
	BlevePath          string
	PrimaryClient      primary.Client
	PrimaryServiceName string
	Events             eventbus.Bus
	DerivedErrors      DerivedErrorReporter
}
