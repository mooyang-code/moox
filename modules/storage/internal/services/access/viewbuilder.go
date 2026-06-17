package access

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
)

func (s *Service) InitViewBuilder() error {
	views, err := s.viewStore()
	if err != nil {
		return err
	}
	view.SetDefaultBuilder(view.NewBuilder(view.Options{
		Metadata: s.metadata,
		Facts:    s.primaryFactReader(),
		Views:    views,
	}))
	return nil
}
