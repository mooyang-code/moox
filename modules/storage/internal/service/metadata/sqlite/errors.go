package sqlite

import "github.com/mooyang-code/moox/modules/storage/internal/storeerr"

var ErrRevisionConflict = storeerr.New(storeerr.Conflict, "dataset revision conflict")
