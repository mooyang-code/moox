package storage

import "github.com/mooyang-code/moox/modules/storage/internal/services/metadata"

type Options struct {
	Root     string
	Metadata metadata.Store
}
