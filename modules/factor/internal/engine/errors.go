package engine

// RetryableError marks transient storage/worker errors.
type RetryableError struct {
	Err error
}

func (e RetryableError) Error() string { return e.Err.Error() }
func (e RetryableError) Unwrap() error { return e.Err }

// NonRetryableError marks factor-code or request validation errors.
type NonRetryableError struct {
	Err error
}

func (e NonRetryableError) Error() string { return e.Err.Error() }
func (e NonRetryableError) Unwrap() error { return e.Err }
