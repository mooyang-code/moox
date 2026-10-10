//go:build linux

package unitpackage

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func extractWithRuntime(t *testing.T, binary string, options ExtractOptions) (Extraction, error) {
	t.Helper()
	ctx, cancel := contextWithExtractionTimeout(t)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "extract", "--archive", options.Archive, "--destination", options.Destination, "--sha256", options.ExpectedSHA256, "--profile", options.Profile).CombinedOutput()
	if err != nil {
		return Extraction{}, err
	}
	var result Extraction
	require.NoError(t, json.Unmarshal(output, &result), string(output))
	return result, nil
}

func TestUnitLinuxRuntimeExtractsSoftwareAndRejectsOtherArchitecture(t *testing.T) {
	binary := os.Getenv("MOOX_RUNTIME_BINARY")
	if binary == "" {
		t.Skip("gate must provide the prebuilt Linux runtime helper")
	}
	for _, mode := range []string{"valid", "wrong-digest", "other-architecture"} {
		t.Run(mode, func(t *testing.T) {
			arch := runtime.GOARCH
			if mode == "other-architecture" {
				arch = "arm64"
				if runtime.GOARCH == "arm64" {
					arch = "amd64"
				}
			}
			built, err := Package(t.Context(), unitFixtureOptions(t, "access", arch))
			require.NoError(t, err)
			parent := extractionParent(t)
			options := ExtractOptions{Archive: built.Archive, Destination: filepath.Join(parent, "release"), ExpectedSHA256: built.SHA256, Profile: "access"}
			if mode == "wrong-digest" {
				options.ExpectedSHA256 = "sha256:" + strings.Repeat("0", 64)
			}
			result, err := extractWithRuntime(t, binary, options)
			if mode == "valid" {
				require.NoError(t, err)
				require.Equal(t, options.Destination, result.Directory)
				requireExtractedSoftware(t, result.Directory, built.Manifest)
			} else {
				require.Error(t, err)
				entries, err := os.ReadDir(parent)
				require.NoError(t, err)
				require.Empty(t, entries)
			}
		})
	}
}

func TestUnitLinuxRuntimeExtractsActualHostPackage(t *testing.T) {
	binary, archive, digest := os.Getenv("MOOX_RUNTIME_BINARY"), os.Getenv("MOOX_HOST_SOFTWARE_PACKAGE"), os.Getenv("MOOX_HOST_PACKAGE_SHA256")
	if binary == "" || archive == "" || digest == "" {
		t.Skip("gate must provide the prebuilt runtime and actual host software package/digest")
	}
	inspected, err := Inspect(t.Context(), archive)
	require.NoError(t, err)
	require.Equal(t, "host", inspected.Manifest.Profile)
	require.Equal(t, digest, inspected.SHA256)
	parent := extractionParent(t)
	destination := filepath.Join(parent, "host release")
	result, err := extractWithRuntime(t, binary, ExtractOptions{Archive: archive, Destination: destination, ExpectedSHA256: digest, Profile: "host"})
	require.NoError(t, err)
	require.Equal(t, inspected, result.Package)
	requireExtractedSoftware(t, destination, inspected.Manifest)
	require.Equal(t, []Component{{ID: "host-gateway", Binary: "bin/moox-host-gateway"}, {ID: "host-agent", Binary: "bin/moox-host-agent"}}, result.Package.Manifest.Components)
	require.Equal(t, unitDigest(requireFile(t, binary)), unitDigest(requireFile(t, filepath.Join(destination, "bin", "moox-runtime"))))
	ctx, cancel := contextWithExtractionTimeout(t)
	defer cancel()
	output, err := exec.CommandContext(ctx, filepath.Join(destination, "bin", "moox-runtime"), "version").CombinedOutput()
	require.NoError(t, err, string(output))
	var version map[string]string
	require.NoError(t, json.Unmarshal(output, &version))
	require.Equal(t, inspected.Manifest.CatalogSHA256, version["catalog_sha256"])
	requireNoUnpublishedExtraction(t, parent)
}

func contextWithExtractionTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 30*time.Second)
}
