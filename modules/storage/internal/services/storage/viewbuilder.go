package storage

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/viewbuilder"
)

func (s *Service) InitViewBuilder() error {
	views, err := s.viewStore()
	if err != nil {
		return err
	}
	viewbuilder.SetDefaultBuilder(viewbuilder.NewBuilder(viewbuilder.Options{
		Metadata: s.metadata,
		Facts:    s.store,
		Views:    views,
	}))
	return nil
}
