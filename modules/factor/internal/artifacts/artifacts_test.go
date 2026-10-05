package artifacts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMaterializeRejectsFactorNameDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "momentum")))
	factor := domain.FactorDef{
		Name: "momentum", SourceCode: "def compute(frame, context): return frame['close']",
	}
	factor.SourceHash = domain.SourceHash(factor.SourceCode)

	_, err := (Artifacts{FactorsDir: root}).Materialize(factor)
	require.Error(t, err)
	entries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}

func TestMaterializeNeverReplacesConflictingHashFile(t *testing.T) {
	root := t.TempDir()
	factor := domain.FactorDef{
		Name: "momentum", SourceCode: "def compute(frame, context): return frame['close']",
	}
	factor.SourceHash = domain.SourceHash(factor.SourceCode)
	dir := filepath.Join(root, factor.Name)
	require.NoError(t, os.Mkdir(dir, 0o755))
	target := filepath.Join(dir, factor.SourceHash+".py")
	require.NoError(t, os.WriteFile(target, []byte("not the hashed source"), 0o644))

	_, err := (Artifacts{FactorsDir: root}).Materialize(factor)
	require.Error(t, err)
	content, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	require.Equal(t, "not the hashed source", string(content))
}

func TestArtifactsMaterializeImmutableSource(t *testing.T) {
	root := t.TempDir()
	artifacts := Artifacts{FactorsDir: root}
	factor := domain.FactorDef{Name: "momentum", SourceCode: "def compute(frame, context): return frame['close']"}
	factor.SourceHash = domain.SourceHash(factor.SourceCode)
	path, err := artifacts.Materialize(factor)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, factor.Name, factor.SourceHash+".py"), path)
	factor.SourceCode = "different source"
	_, err = artifacts.Materialize(factor)
	require.ErrorContains(t, err, "source hash")
}
