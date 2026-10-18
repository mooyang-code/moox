package command

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/stretchr/testify/require"
)

func TestSetupPackageAndInspectUseNoOperatorCredentials(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	folder := t.TempDir()
	binaries := filepath.Join(folder, "compiled binaries")
	require.NoError(t, os.MkdirAll(binaries, 0o755))
	elf := make([]byte, 64)
	copy(elf, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(elf[16:], 2)
	binary.LittleEndian.PutUint16(elf[18:], 62)
	require.NoError(t, os.WriteFile(filepath.Join(binaries, "moox-access"), elf, 0o755))
	output := filepath.Join(folder, "access package.tar.gz")
	var built unitpackage.Result
	for _, args := range [][]string{
		{"package", "--repo-root", root, "--profile", "access", "--binary-dir", binaries, "--output", output},
		{"package", "inspect", output},
	} {
		command := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) {
			t.Fatal("packaging must not read moox.toml")
			return nil, nil
		}})
		var buffer bytes.Buffer
		command.SetOut(&buffer)
		command.SetErr(&buffer)
		command.SetArgs(args)
		require.NoError(t, command.Execute(), buffer.String())
		var result unitpackage.Result
		require.NoError(t, json.Unmarshal(buffer.Bytes(), &result))
		if built.Archive == "" {
			built = result
		} else {
			require.Equal(t, built, result)
		}
		require.Equal(t, "access", result.Manifest.Profile)
		require.Equal(t, []unitpackage.Component{{ID: "access", Binary: "bin/moox-access"}}, result.Manifest.Components)
	}
}

func TestSetupPackageRequiresProfileAndOutput(t *testing.T) {
	for _, args := range [][]string{{"package"}, {"package", "--profile", "access"}, {"package", "--output", "access.tar.gz"}, {"package", "inspect"}} {
		command := newSetupCommand(setupDeps{})
		var buffer bytes.Buffer
		command.SetOut(&buffer)
		command.SetErr(&buffer)
		command.SetArgs(args)
		require.Error(t, command.Execute())
	}
}
