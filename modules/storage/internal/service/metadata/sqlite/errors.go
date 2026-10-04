package sqlite

import "errors"

var (
	ErrRevisionConflict       = errors.New("dataset revision conflict")
	ErrBindingLocked          = errors.New("dataset binding is locked")
	ErrDatasetMustBeDisabled  = errors.New("dataset must be disabled")
	ErrDataNodeDisabled       = errors.New("data node is disabled")
	ErrDataNodeReferenced     = errors.New("data node still has datasets")
	ErrDataNodeMustBeDisabled = errors.New("data node must be disabled")
)
