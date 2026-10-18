package unitpackage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func extractionParent(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	// testing creates its leaf directory using the host umask. Linux build
	// hosts may use 0002; deployment parents must explicitly be private.
	require.NoError(t, os.Chmod(parent, 0o700))
	return parent
}

func requireNoUnpublishedExtraction(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasPrefix(entry.Name(), ".extract-"), entry.Name())
	}
}

func requireExtractionRejected(t *testing.T, archive, profile, arch string) {
	t.Helper()
	parent := extractionParent(t)
	destination := filepath.Join(parent, "release")
	_, err := Extract(t.Context(), ExtractOptions{Archive: archive, Destination: destination, ExpectedSHA256: unitDigest(requireFile(t, archive)), Profile: profile, GOOS: "linux", GOARCH: arch})
	require.Error(t, err)
	_, err = os.Lstat(destination)
	require.True(t, os.IsNotExist(err))
	requireNoUnpublishedExtraction(t, parent)
}

func requireExtractedSoftware(t *testing.T, destination string, manifest Manifest) {
	t.Helper()
	expected := map[string]File{}
	for _, file := range manifest.Files {
		expected[file.Path] = file
	}
	var count int
	require.NoError(t, filepath.Walk(destination, func(name string, info os.FileInfo, err error) error {
		require.NoError(t, err)
		if info.IsDir() {
			require.Equal(t, os.FileMode(0o700), info.Mode().Perm(), name)
			return nil
		}
		require.True(t, info.Mode().IsRegular(), name)
		relative, err := filepath.Rel(destination, name)
		require.NoError(t, err)
		if relative == unitManifestName {
			require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
			return nil
		}
		declared, exists := expected[filepath.ToSlash(relative)]
		require.True(t, exists, relative)
		require.Equal(t, os.FileMode(declared.Mode), info.Mode().Perm(), relative)
		require.Equal(t, declared.Size, info.Size(), relative)
		digest := sha256.Sum256(requireFile(t, name))
		require.Equal(t, declared.SHA256, "sha256:"+hex.EncodeToString(digest[:]), relative)
		count++
		return nil
	}))
	require.Equal(t, len(expected), count)
}

func TestUnitExtractionPublishesVerifiedSoftwareForAllProfiles(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("atomic software extraction requires Linux or macOS")
	}
	for _, profile := range []string{"host", "control", "storage", "access", "egress-proxy", "trade"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(profile+"/"+arch, func(t *testing.T) {
				built, err := Package(t.Context(), unitFixtureOptions(t, profile, arch))
				require.NoError(t, err)
				parent := extractionParent(t)
				destination := filepath.Join(parent, "new release")
				result, err := Extract(t.Context(), ExtractOptions{Archive: built.Archive, Destination: destination, ExpectedSHA256: built.SHA256, Profile: profile, GOOS: "linux", GOARCH: arch})
				require.NoError(t, err)
				require.Equal(t, destination, result.Directory)
				require.Equal(t, built, result.Package)
				requireExtractedSoftware(t, destination, built.Manifest)
				requireNoUnpublishedExtraction(t, parent)
				_, err = os.Lstat(filepath.Join(destination, "runtime.json"))
				require.True(t, os.IsNotExist(err), "software extraction must not invent private runtime configuration")
				_, err = os.Lstat(filepath.Join(destination, "run"))
				require.True(t, os.IsNotExist(err), "software extraction must not start or activate services")
			})
		}
	}
}

func TestUnitExtractionRejectsUnexpectedMetadataAndCancellation(t *testing.T) {
	built, err := Package(t.Context(), unitFixtureOptions(t, "access", "amd64"))
	require.NoError(t, err)
	for name, mutate := range map[string]func(*ExtractOptions){
		"digest mismatch":       func(o *ExtractOptions) { o.ExpectedSHA256 = "sha256:" + strings.Repeat("0", 64) },
		"malformed digest":      func(o *ExtractOptions) { o.ExpectedSHA256 = strings.Repeat("0", 64) },
		"uppercase digest":      func(o *ExtractOptions) { o.ExpectedSHA256 = strings.ToUpper(built.SHA256) },
		"profile mismatch":      func(o *ExtractOptions) { o.Profile = "host" },
		"architecture mismatch": func(o *ExtractOptions) { o.GOARCH = "arm64" },
		"unsupported platform":  func(o *ExtractOptions) { o.GOOS = "darwin" },
		"relative destination":  func(o *ExtractOptions) { o.Destination = "release" },
	} {
		t.Run(name, func(t *testing.T) {
			parent := extractionParent(t)
			options := ExtractOptions{Archive: built.Archive, Destination: filepath.Join(parent, "release"), ExpectedSHA256: built.SHA256, Profile: "access", GOOS: "linux", GOARCH: "amd64"}
			mutate(&options)
			_, err := Extract(t.Context(), options)
			require.Error(t, err)
			entries, err := os.ReadDir(parent)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
	parent := extractionParent(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Extract(ctx, ExtractOptions{Archive: built.Archive, Destination: filepath.Join(parent, "release"), ExpectedSHA256: built.SHA256, Profile: "access", GOOS: "linux", GOARCH: "amd64"})
	require.ErrorIs(t, err, context.Canceled)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	require.Empty(t, entries)
	truncated := filepath.Join(t.TempDir(), "truncated.tar.gz")
	raw := requireFile(t, built.Archive)
	require.NoError(t, os.WriteFile(truncated, raw[:len(raw)/2], 0o600))
	requireExtractionRejected(t, truncated, "access", "amd64")
}

func TestUnitExtractionPreservesExistingObjectsAndConcurrentWinner(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("atomic software extraction requires Linux or macOS")
	}
	built, err := Package(t.Context(), unitFixtureOptions(t, "access", "amd64"))
	require.NoError(t, err)
	for _, kind := range []string{"file", "empty-directory", "nonempty-directory", "link", "parent-link", "unsafe-parent"} {
		t.Run(kind, func(t *testing.T) {
			parent := extractionParent(t)
			destination := filepath.Join(parent, "release")
			witness := filepath.Join(parent, "witness")
			require.NoError(t, os.WriteFile(witness, []byte("preserved"), 0o600))
			switch kind {
			case "file":
				require.NoError(t, os.WriteFile(destination, []byte("preserved"), 0o600))
			case "empty-directory", "nonempty-directory":
				require.NoError(t, os.Mkdir(destination, 0o700))
				if kind == "nonempty-directory" {
					require.NoError(t, os.WriteFile(filepath.Join(destination, "original"), []byte("preserved"), 0o600))
				}
			case "link":
				require.NoError(t, os.Symlink(witness, destination))
			case "parent-link":
				link := filepath.Join(parent, "parent-view")
				require.NoError(t, os.Symlink(parent, link))
				destination = filepath.Join(link, "release")
			case "unsafe-parent":
				require.NoError(t, os.Chmod(parent, 0o777))
			}
			before, err := os.ReadDir(parent)
			require.NoError(t, err)
			_, err = Extract(t.Context(), ExtractOptions{Archive: built.Archive, Destination: destination, ExpectedSHA256: built.SHA256, Profile: "access", GOOS: "linux", GOARCH: "amd64"})
			require.Error(t, err)
			after, err := os.ReadDir(parent)
			require.NoError(t, err)
			require.Equal(t, len(before), len(after))
			require.Equal(t, "preserved", string(requireFile(t, witness)))
			if kind == "file" {
				require.Equal(t, "preserved", string(requireFile(t, destination)))
			}
			if kind == "nonempty-directory" {
				require.Equal(t, "preserved", string(requireFile(t, filepath.Join(destination, "original"))))
			}
		})
	}
	parent := extractionParent(t)
	destination := filepath.Join(parent, "same-release")
	options := ExtractOptions{Archive: built.Archive, Destination: destination, ExpectedSHA256: built.SHA256, Profile: "access", GOOS: "linux", GOARCH: "amd64"}
	barrier := make(chan struct{})
	errors := make(chan error, 8)
	for range 8 {
		go func() { <-barrier; _, err := Extract(t.Context(), options); errors <- err }()
	}
	close(barrier)
	var successes int
	for range 8 {
		if <-errors == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
	requireExtractedSoftware(t, destination, built.Manifest)
	requireNoUnpublishedExtraction(t, parent)
}
