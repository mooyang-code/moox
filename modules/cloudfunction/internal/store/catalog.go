package store

import (
	"errors"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"gorm.io/gorm"
)

var ErrPollingInstanceNotFound = errors.New("polling instance not found")

type CatalogRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewCatalogRepository(db *gorm.DB) *CatalogRepository {
	return &CatalogRepository{db: db, now: time.Now}
}

func (r *CatalogRepository) currentTime() time.Time {
	if r != nil && r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func pageFromCommon(page *pb.Page) (int, int) {
	if page == nil {
		return 1, 50
	}
	p, s := int(page.GetPage()), int(page.GetSize())
	if p <= 0 {
		p = 1
	}
	if s <= 0 {
		s = 50
	}
	if s > 1000 {
		s = 1000
	}
	return p, s
}
