package repository

import (
	"errors"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
)

var ErrPollingNodeNotFound = errors.New("polling node not found")

type CatalogRepository struct{ db *gorm.DB }

func NewCatalogRepository(db *gorm.DB) *CatalogRepository { return &CatalogRepository{db: db} }

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
