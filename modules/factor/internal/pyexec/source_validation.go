package pyexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// ValidateSource loads a factor module using the configured Python runtime and
// verifies that its compute callable accepts the worker's three arguments.
func ValidateSource(ctx context.Context, pythonBin, sourcePath string) error {
	if pythonBin == "" || sourcePath == "" {
		return errors.New("python binary and factor source path are required")
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	return ValidateSourceCode(ctx, pythonBin, string(source))
}

// ValidateSourceCode validates the exact content the catalog will persist.
func ValidateSourceCode(ctx context.Context, pythonBin, source string) error {
	if pythonBin == "" {
		return errors.New("python binary is required")
	}
	tmp, err := os.CreateTemp("", "moox-factor-validate-*.py")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.WriteString(source); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	const validator = `import hashlib,importlib.util,inspect,pathlib,sys
p=pathlib.Path(sys.argv[1])
raw=p.read_bytes()
name="moox_factor_"+hashlib.sha256(raw).hexdigest()
spec=importlib.util.spec_from_file_location(name,p)
if spec is None or spec.loader is None: raise ImportError("cannot load factor source")
module=importlib.util.module_from_spec(spec)
sys.modules[name]=module
spec.loader.exec_module(module)
compute=getattr(module,"compute",None)
if not callable(compute): raise TypeError("compute(df, params, context) must be callable")
inspect.signature(compute).bind(None,None,None)`
	cmd := exec.CommandContext(ctx, pythonBin, "-c", validator, path)
	if err := cmd.Run(); err != nil {
		return errors.New("source must load and define callable compute(df, params, context)")
	}
	return nil
}
