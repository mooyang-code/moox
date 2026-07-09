package repository

type Page struct {
	Page     int
	PageSize int
}

func (p Page) limit() int {
	return p.Limit()
}

func (p Page) Limit() int {
	if p.PageSize <= 0 {
		return 50
	}
	if p.PageSize > 500 {
		return 500
	}
	return p.PageSize
}

func (p Page) offset() int {
	if p.Page <= 1 {
		return 0
	}
	return (p.Page - 1) * p.Limit()
}
