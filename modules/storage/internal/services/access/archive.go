package access

import (
	"path/filepath"

	"github.com/mooyang-code/moox/modules/storage/internal/services/archive"
)

func (s *Service) InitArchiveService() error {
	archiveRoot := s.parquetPath
	if archiveRoot == "" {
		archiveRoot = filepath.Join(s.root, "archive")
	}
	archive.SetDefaultService(archive.NewService(archive.Options{
		Metadata:    s.metadata,
		Facts:       s.primaryFactReader(),
		ArchiveRoot: archiveRoot,
	}))
	return nil
}
