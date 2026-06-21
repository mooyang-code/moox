package access

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
)

type ViewErrorReporter func(ctx context.Context, stage string, err error)

// Options 保存 Access 服务创建时的依赖与路径配置。
type Options struct {
	Root               string
	Metadata           metadata.Store
	MetadataReader     metadata.Reader
	MetadataPath       string
	InitSchemaPath     string
	PebblePath         string
	DuckDBPath         string
	BlevePath          string
	ParquetPath        string
	PrimaryClient      primary.Client
	PrimaryServiceName string
	Events             eventbus.Bus
	ViewErrors         ViewErrorReporter
}
