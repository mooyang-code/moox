package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unitRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../../..")
	require.NoError(t, err)
	return root
}

func unitELFFixture(arch string) []byte {
	raw := make([]byte, 120)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	machine := uint16(62)
	if arch == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(raw[18:], machine)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], 0x401000)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint32(raw[64:], 1)
	binary.LittleEndian.PutUint32(raw[68:], 5)
	binary.LittleEndian.PutUint64(raw[80:], 0x400000)
	binary.LittleEndian.PutUint64(raw[96:], uint64(len(raw)))
	binary.LittleEndian.PutUint64(raw[104:], uint64(len(raw)))
	return raw
}

func unitFixtureOptions(t *testing.T, profile, arch string) UnitPackageOptions {
	t.Helper()
	binaries := t.TempDir()
	names, err := UnitBinaries(profile)
	require.NoError(t, err)
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(binaries, name), unitELFFixture(arch), 0o755))
	}
	return UnitPackageOptions{RepositoryRoot: unitRepoRoot(t), BinaryDirectory: binaries, Output: filepath.Join(t.TempDir(), profile+".tar.gz"), Profile: profile, GOOS: "linux", GOARCH: arch}
}

func TestUnitPackagesHaveExactComponentBoundariesAndRepeatableDigests(t *testing.T) {
	for _, profile := range []string{"host", "control", "storage", "access", "egress-proxy", "trade"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(profile+"/"+arch, func(t *testing.T) {
				opts := unitFixtureOptions(t, profile, arch)
				// Unrelated executables in a shared build directory must never leak.
				require.NoError(t, os.WriteFile(filepath.Join(opts.BinaryDirectory, "moox-unrelated"), unitELFFixture(arch), 0o755))
				result, err := PackageUnit(t.Context(), opts)
				require.NoError(t, err)
				inspected, err := InspectUnitPackage(t.Context(), result.Archive)
				require.NoError(t, err)
				assert.Equal(t, result, inspected)
				info, err := os.Stat(result.Archive)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
				var ids []string
				for _, component := range result.Manifest.Components {
					ids = append(ids, component.ID)
				}
				if profile == "host" {
					assert.Equal(t, []string{"host-gateway", "host-agent"}, ids)
				} else {
					assert.NotContains(t, ids, "host-gateway")
					assert.NotContains(t, ids, "host-agent")
				}
				if profile == "storage" {
					assert.Equal(t, []string{"storage-primary", "storage-node", "storage-view"}, ids)
				}
				if profile == "control" {
					assert.Contains(t, ids, "console-proxy")
					assert.Contains(t, ids, "web-host")
					assert.NotContains(t, ids, "trade")
					assert.NotContains(t, ids, "access")
				}
				for _, file := range result.Manifest.Files {
					assert.NotContains(t, file.Path, "moox-unrelated")
					assert.NotContains(t, file.Path, "gateway-control.key")
					assert.NotContains(t, file.Path, "Caddyfile")
					assert.NotContains(t, file.Path, "moox.toml")
					assert.False(t, strings.HasPrefix(file.Path, "secrets/") || strings.HasPrefix(file.Path, "data/"))
				}
				first, err := os.ReadFile(result.Archive)
				require.NoError(t, err)
				again, err := PackageUnit(t.Context(), opts)
				require.NoError(t, err)
				second, err := os.ReadFile(again.Archive)
				require.NoError(t, err)
				assert.Equal(t, first, second)
				assert.Equal(t, result.SHA256, again.SHA256)
			})
		}
	}
}

func unitPrivateRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{
		"packages/servicecatalog/catalog.yaml": servicecatalog.EmbeddedYAML(),
		"modules/access/config/app.yaml":       []byte("verification_file: ../../secrets/access/access-verification.json\n"),
		"modules/access/config/trpc_go.yaml":   []byte("server: {}\n"),
		".gitignore":                           []byte("moox.toml\nmodules/access/config/operator.yaml\n"),
	}
	for name, raw := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), raw, 0o644))
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		raw, err := command.CombinedOutput()
		require.NoError(t, err, string(raw))
	}
	return root
}

func TestUnitPackagingExcludesIgnoredCredentialsAndRejectsSourceLinks(t *testing.T) {
	opts := unitFixtureOptions(t, "access", "amd64")
	opts.RepositoryRoot = unitPrivateRepo(t)
	for _, name := range []string{"moox.toml", "modules/access/config/operator.yaml"} {
		require.NoError(t, os.WriteFile(filepath.Join(opts.RepositoryRoot, name), []byte("private-operator-secret"), 0o600))
	}
	result, err := PackageUnit(t.Context(), opts)
	require.NoError(t, err)
	for _, entry := range readUnitEntries(t, result.Archive) {
		assert.NotContains(t, string(entry.raw), "private-operator-secret")
	}
	config := filepath.Join(opts.RepositoryRoot, "modules/access/config/app.yaml")
	require.NoError(t, os.Remove(config))
	require.NoError(t, os.Symlink(filepath.Join(opts.RepositoryRoot, "moox.toml"), config))
	_, err = PackageUnit(t.Context(), opts)
	require.ErrorContains(t, err, "symbolic links")
	_, err = InspectUnitPackage(t.Context(), result.Archive)
	require.NoError(t, err, "a failed package attempt must keep the previous valid archive")
}

func TestUnitPackagingRejectsWrongTargetAndPreservesSourcesAndOutputs(t *testing.T) {
	for name, change := range map[string]func(*UnitPackageOptions){
		"wrong architecture":   func(o *UnitPackageOptions) { o.GOARCH = "arm64" },
		"wrong platform":       func(o *UnitPackageOptions) { o.GOOS = "darwin" },
		"unknown profile":      func(o *UnitPackageOptions) { o.Profile = "gateway" },
		"configuration output": func(o *UnitPackageOptions) { o.Output = filepath.Join(o.RepositoryRoot, "moox.toml") },
		"source catalog mismatch": func(o *UnitPackageOptions) {
			o.RepositoryRoot = unitPrivateRepo(t)
			require.NoError(t, os.WriteFile(filepath.Join(o.RepositoryRoot, "packages/servicecatalog/catalog.yaml"), []byte("version: 99\n"), 0o644))
		},
		"non executable": func(o *UnitPackageOptions) {
			require.NoError(t, os.Chmod(filepath.Join(o.BinaryDirectory, "moox-access"), 0o644))
		},
		"script binary": func(o *UnitPackageOptions) {
			require.NoError(t, os.WriteFile(filepath.Join(o.BinaryDirectory, "moox-access"), []byte(strings.Repeat("#!/bin/sh\n", 16)), 0o755))
		},
	} {
		t.Run(name, func(t *testing.T) {
			opts := unitFixtureOptions(t, "access", "amd64")
			previous := opts.Output
			require.NoError(t, os.WriteFile(previous, []byte("previous-archive"), 0o600))
			change(&opts)
			_, err := PackageUnit(t.Context(), opts)
			require.Error(t, err)
			raw, err := os.ReadFile(previous)
			require.NoError(t, err)
			assert.Equal(t, "previous-archive", string(raw))
		})
	}
	opts := unitFixtureOptions(t, "access", "amd64")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := PackageUnit(ctx, opts)
	require.Error(t, err)
	_, err = os.Stat(opts.Output)
	require.ErrorIs(t, err, os.ErrNotExist)
}

type unitArchiveEntry struct {
	header tar.Header
	raw    []byte
}

func readUnitEntries(t *testing.T, archive string) []unitArchiveEntry {
	t.Helper()
	file, err := os.Open(archive)
	require.NoError(t, err)
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var entries []unitArchiveEntry
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		raw, err := io.ReadAll(reader)
		require.NoError(t, err)
		entries = append(entries, unitArchiveEntry{*header, raw})
	}
	return entries
}
func rewriteUnitEntries(t *testing.T, entries []unitArchiveEntry) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "changed.tar.gz")
	file, err := os.Create(archive)
	require.NoError(t, err)
	compressed := gzip.NewWriter(file)
	writer := tar.NewWriter(compressed)
	for _, entry := range entries {
		entry.header.Size = int64(len(entry.raw))
		require.NoError(t, writer.WriteHeader(&entry.header))
		_, err := writer.Write(entry.raw)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, compressed.Close())
	require.NoError(t, file.Close())
	return archive
}
func rehashUnitManifest(t *testing.T, entries []unitArchiveEntry) {
	t.Helper()
	var manifest UnitManifest
	require.NoError(t, json.Unmarshal(entries[len(entries)-1].raw, &manifest))
	manifest.Files = nil
	for _, entry := range entries[:len(entries)-1] {
		manifest.Files = append(manifest.Files, UnitFile{Path: entry.header.Name, SHA256: unitDigest(entry.raw), Mode: uint32(entry.header.Mode), Size: int64(len(entry.raw))})
	}
	slices.SortFunc(manifest.Files, func(a, b UnitFile) int { return strings.Compare(a.Path, b.Path) })
	raw, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	entries[len(entries)-1].raw = append(raw, '\n')
}

func TestUnitInspectionRejectsTamperingAndProfileEscapes(t *testing.T) {
	opts := unitFixtureOptions(t, "access", "amd64")
	result, err := PackageUnit(t.Context(), opts)
	require.NoError(t, err)
	for name, mutate := range map[string]func([]unitArchiveEntry) []unitArchiveEntry{
		"corrupted file": func(entries []unitArchiveEntry) []unitArchiveEntry {
			entries[0].raw = []byte("changed")
			return entries
		},
		"wrong architecture with new digest": func(entries []unitArchiveEntry) []unitArchiveEntry {
			for index := range entries {
				if entries[index].header.Name == "bin/moox-access" {
					binary.LittleEndian.PutUint16(entries[index].raw[18:], 183)
				}
			}
			rehashUnitManifest(t, entries)
			return entries
		},
		"duplicate file": func(entries []unitArchiveEntry) []unitArchiveEntry {
			return append([]unitArchiveEntry{entries[0]}, entries...)
		},
		"path traversal": func(entries []unitArchiveEntry) []unitArchiveEntry {
			entries[0].header.Name = "../escape"
			return entries
		},
		"link": func(entries []unitArchiveEntry) []unitArchiveEntry {
			entries[0].header.Typeflag = tar.TypeSymlink
			entries[0].header.Linkname = "/tmp/escape"
			entries[0].raw = nil
			return entries
		},
		"profile binary injection": func(entries []unitArchiveEntry) []unitArchiveEntry {
			extra := entries[0]
			extra.header.Name = "bin/moox-host-gateway"
			entries = append(entries[:len(entries)-1], extra, entries[len(entries)-1])
			rehashUnitManifest(t, entries)
			return entries
		},
		"runtime key injection": func(entries []unitArchiveEntry) []unitArchiveEntry {
			extra := unitArchiveEntry{tar.Header{Name: "access/secrets/private.json", Mode: 0o644, Typeflag: tar.TypeReg}, []byte("private")}
			entries = append(entries[:len(entries)-1], extra, entries[len(entries)-1])
			rehashUnitManifest(t, entries)
			return entries
		},
		"missing app config": func(entries []unitArchiveEntry) []unitArchiveEntry {
			filtered := slices.DeleteFunc(entries, func(entry unitArchiveEntry) bool { return entry.header.Name == "access/config/app.yaml" })
			rehashUnitManifest(t, filtered)
			return filtered
		},
		"duplicate JSON key": func(entries []unitArchiveEntry) []unitArchiveEntry {
			last := &entries[len(entries)-1]
			last.raw = bytes.Replace(last.raw, []byte("{\n"), []byte("{\n  \"version\": 1,\n"), 1)
			return entries
		},
	} {
		t.Run(name, func(t *testing.T) {
			entries := mutate(readUnitEntries(t, result.Archive))
			archive := rewriteUnitEntries(t, entries)
			_, err := InspectUnitPackage(t.Context(), archive)
			require.Error(t, err)
		})
	}
	for _, suffix := range [][]byte{[]byte("extra"), requireFile(t, result.Archive)} {
		original := requireFile(t, result.Archive)
		archive := filepath.Join(t.TempDir(), "concatenated.tar.gz")
		require.NoError(t, os.WriteFile(archive, append(original, suffix...), 0o600))
		_, err := InspectUnitPackage(t.Context(), archive)
		require.ErrorContains(t, err, "trailing")
	}
}

func TestUnitInspectionRejectsDataAfterTarTerminator(t *testing.T) {
	opts := unitFixtureOptions(t, "access", "amd64")
	result, err := PackageUnit(t.Context(), opts)
	require.NoError(t, err)
	original := requireFile(t, result.Archive)
	reader, err := gzip.NewReader(bytes.NewReader(original))
	require.NoError(t, err)
	unpacked, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	var changed bytes.Buffer
	writer := gzip.NewWriter(&changed)
	_, err = writer.Write(append(unpacked, bytes.Repeat([]byte{0}, 64<<10)...))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	archive := filepath.Join(t.TempDir(), "extra-tar-data.tar.gz")
	require.NoError(t, os.WriteFile(archive, changed.Bytes(), 0o600))
	_, err = InspectUnitPackage(t.Context(), archive)
	require.ErrorContains(t, err, "trailing deployment tar data")
}
