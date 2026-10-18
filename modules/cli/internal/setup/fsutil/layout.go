package fsutil

import (
	"errors"
	"path/filepath"
	"strings"
)

// ValidateUnitRoots checks the layout before creating directories or private
// state. Unit roots may be nested, but may not contain each other or occupy a
// deployment namespace used for journals, identities or transfers.
func ValidateUnitRoots(deployment string, units ...string) error {
	clean := func(name string) bool {
		return filepath.IsAbs(name) && filepath.Clean(name) == name && name != string(filepath.Separator) && !strings.ContainsAny(name, "\x00\r\n")
	}
	if !clean(deployment) {
		return errors.New("deployment root requires a clean absolute directory")
	}
	for i, unit := range units {
		relative, err := filepath.Rel(deployment, unit)
		if !clean(unit) || err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("unit roots must be strict descendants of deployment root")
		}
		first := strings.Split(relative, string(filepath.Separator))[0]
		switch first {
		case "bootstrap", "bootstrap-input", "identity", "run":
			return errors.New("unit roots must not occupy deployment state namespaces")
		}
		for _, previous := range units[:i] {
			if previous == unit || strings.HasPrefix(previous, unit+string(filepath.Separator)) || strings.HasPrefix(unit, previous+string(filepath.Separator)) {
				return errors.New("unit roots must be independent directories")
			}
		}
	}
	return nil
}
