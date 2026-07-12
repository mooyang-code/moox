package access

import "errors"

func errText(message string) error {
	return errors.New(message)
}
