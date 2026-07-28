package store

import (
	"errors"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
)

var ErrPollingNodeNotFound = errors.New("polling node not found")

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
