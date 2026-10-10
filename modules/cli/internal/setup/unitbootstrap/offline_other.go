//go:build !linux

package unitbootstrap

import (
	"errors"
	"os/exec"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

func offlineProcess(*exec.Cmd, unitruntime.Options) (func(), error) {
	return nil, errors.New("offline bootstrap execution requires Linux")
}
