package unitinstall

import (
	"errors"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
)

func writeScripts(root *os.Root, options PrepareOptions) error {
	helper := `"${release}/bin/moox-runtime"`
	if options.Profile != "host" {
		helper = "'" + strings.ReplaceAll(options.HostUnitRoot+"/current/bin/moox-runtime", "'", "'\\''") + "'"
	}
	for _, operation := range []string{"start", "stop", "restart", "pause", "resume", "healthcheck", "status"} {
		raw := []byte("#!/usr/bin/env bash\nset -euo pipefail\nrelease=\"$(cd -- \"$(dirname -- \"${BASH_SOURCE[0]}\")\" && pwd -P)\"\nexec " + helper + " " + operation + " --plan \"${release}/runtime.json\" \"$@\"\n")
		name := operation + ".sh"
		if err := fsutil.WritePrivate(root, name, raw, false); err != nil {
			return err
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		if err := errors.Join(file.Chmod(0o755), file.Sync(), file.Close()); err != nil {
			return err
		}
	}
	return nil
}
