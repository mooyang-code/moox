package unitpackage

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// Inspect verifies every entry without extracting the archive.
func Inspect(ctx context.Context, archive string) (result Result, returnErr error) {
	return inspect(ctx, archive, nil)
}

type entrySink func(*tar.Header) (*os.File, error)

func inspect(ctx context.Context, archive string, sink entrySink) (result Result, returnErr error) {
	absolute, err := filepath.Abs(archive)
	if err != nil {
		return result, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return result, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return result, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxUnitBytes {
		return result, fmt.Errorf("invalid deployment archive")
	}
	archiveDigest := sha256.New()
	buffer := bufio.NewReader(io.TeeReader(file, archiveDigest))
	compressed, err := gzip.NewReader(buffer)
	if err != nil {
		return result, fmt.Errorf("invalid deployment archive compression")
	}
	defer compressed.Close()
	compressed.Multistream(false)
	reader := tar.NewReader(compressed)
	observed := make(map[string]File)
	binaryHeaders := make(map[string][]byte)
	var manifest *Manifest
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, fmt.Errorf("invalid deployment tar archive: %w", err)
		}
		if !validUnitPath(header.Name) || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxUnitFileBytes || header.Mode != 0o644 && header.Mode != 0o755 || manifest != nil {
			return result, fmt.Errorf("invalid deployment archive entry")
		}
		if _, exists := observed[header.Name]; exists {
			return result, fmt.Errorf("duplicate deployment archive entry")
		}
		total += header.Size
		if total > maxUnitBytes || len(observed) >= maxUnitFiles && header.Name != unitManifestName {
			return result, fmt.Errorf("deployment archive exceeds limits")
		}
		if header.Name == unitManifestName {
			if header.Size > 4<<20 || header.Mode != 0o644 {
				return result, fmt.Errorf("invalid deployment manifest")
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				return result, err
			}
			var decoded Manifest
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&decoded); err != nil {
				return result, fmt.Errorf("invalid deployment manifest: %w", err)
			}
			if decoder.Decode(new(any)) != io.EOF {
				return result, fmt.Errorf("invalid trailing deployment manifest data")
			}
			canonical, err := json.MarshalIndent(decoded, "", "  ")
			if err != nil || !bytes.Equal(raw, append(canonical, '\n')) {
				return result, fmt.Errorf("deployment manifest must use canonical encoding")
			}
			manifest = &decoded
			if sink != nil {
				if _, _, err := readEntry(ctx, bytes.NewReader(raw), header, sink); err != nil {
					return result, err
				}
			}
			continue
		}
		entry, prefix, err := readEntry(ctx, reader, header, sink)
		if err != nil {
			return result, err
		}
		observed[header.Name] = entry
		if prefix != nil {
			binaryHeaders[header.Name] = prefix
		}
	}
	// Read through the checksum and reject concatenated/trailing payloads.
	var trailing [1]byte
	if n, err := io.ReadFull(compressed, trailing[:]); err != io.EOF || n != 0 {
		return result, fmt.Errorf("invalid trailing deployment tar data")
	}
	if _, err := buffer.Peek(1); err != io.EOF {
		return result, fmt.Errorf("invalid trailing deployment compressed data")
	}
	if manifest == nil {
		return result, fmt.Errorf("deployment manifest is missing")
	}
	if err := validateUnitManifest(*manifest, observed); err != nil {
		return result, err
	}
	for name, header := range binaryHeaders {
		if !validUnitELF(header, manifest.GOARCH) {
			return result, fmt.Errorf("deployment binary %s does not match the declared target platform", name)
		}
	}
	after, err := file.Stat()
	if err != nil || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return result, fmt.Errorf("deployment archive changed while inspecting")
	}
	return Result{Archive: absolute, SHA256: "sha256:" + hex.EncodeToString(archiveDigest.Sum(nil)), Manifest: *manifest}, nil
}

// Inspect and Extract consume the very same byte stream and validation path.
// A sink may write only into an unpublished private staging directory.
func readEntry(ctx context.Context, reader io.Reader, header *tar.Header, sink entrySink) (File, []byte, error) {
	digest := sha256.New()
	var writer io.Writer = digest
	var file *os.File
	if sink != nil {
		var err error
		file, err = sink(header)
		if err != nil {
			return File{}, nil, err
		}
		defer file.Close()
		writer = io.MultiWriter(digest, file)
	}
	writer = &unitContextWriter{ctx: ctx, writer: writer}
	var prefix []byte
	var count int64
	if strings.HasPrefix(header.Name, "bin/") {
		prefix = make([]byte, 64)
		if _, err := io.ReadFull(reader, prefix); err != nil {
			return File{}, nil, fmt.Errorf("deployment binary is truncated")
		}
		if _, err := writer.Write(prefix); err != nil {
			return File{}, nil, err
		}
		count = int64(len(prefix))
	}
	n, err := io.Copy(writer, reader)
	if err != nil {
		return File{}, nil, err
	}
	if count+n != header.Size {
		return File{}, nil, fmt.Errorf("deployment entry size differs from its header")
	}
	if file != nil {
		if err := file.Chmod(os.FileMode(header.Mode)); err != nil {
			return File{}, nil, err
		}
		if err := file.Sync(); err != nil {
			return File{}, nil, err
		}
		if err := file.Close(); err != nil {
			return File{}, nil, err
		}
	}
	return File{Path: header.Name, SHA256: "sha256:" + hex.EncodeToString(digest.Sum(nil)), Mode: uint32(header.Mode), Size: header.Size}, prefix, nil
}
func validateUnitManifest(manifest Manifest, observed map[string]File) error {
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported deployment manifest version")
	}
	if err := unitPlatform(manifest.GOOS, manifest.GOARCH); err != nil {
		return err
	}
	components, err := Components(manifest.Profile)
	if err != nil {
		return err
	}
	if !slices.Equal(manifest.Components, components) {
		return fmt.Errorf("deployment manifest component boundary does not match the profile")
	}
	if manifest.CatalogSHA256 != unitDigest(servicecatalog.EmbeddedYAML()) {
		return fmt.Errorf("deployment manifest catalog differs from this CLI")
	}
	if catalog, ok := observed["config/servicecatalog/catalog.yaml"]; !ok || catalog.SHA256 != manifest.CatalogSHA256 {
		return fmt.Errorf("deployment package catalog is missing or corrupted")
	}
	names, err := Binaries(manifest.Profile)
	if err != nil {
		return err
	}
	binaries := make(map[string]bool, len(names))
	for _, name := range names {
		binaries["bin/"+name] = true
		if file, ok := observed["bin/"+name]; !ok || file.Mode != 0o755 {
			return fmt.Errorf("deployment binary %s is missing or not executable", name)
		}
	}
	if len(manifest.Files) != len(observed) {
		return fmt.Errorf("deployment manifest file count mismatch")
	}
	previous := ""
	for _, declared := range manifest.Files {
		if !validUnitPath(declared.Path) || declared.Path <= previous {
			return fmt.Errorf("deployment manifest paths must be unique and sorted")
		}
		previous = declared.Path
		if actual, ok := observed[declared.Path]; !ok || actual != declared {
			return fmt.Errorf("deployment package file verification failed: %s", declared.Path)
		}
		if strings.HasPrefix(declared.Path, "bin/") {
			if !binaries[declared.Path] {
				return fmt.Errorf("deployment package contains a binary outside its profile")
			}
			continue
		}
		if declared.Path == "config/servicecatalog/catalog.yaml" {
			continue
		}
		allowed := false
		for _, component := range components {
			for _, asset := range unitSpecs[component.ID].assets {
				if declared.Path == asset.destination && !asset.tree || asset.tree && strings.HasPrefix(declared.Path, asset.destination+"/") && unitAssetExtension(declared.Path) {
					allowed = true
					break
				}
			}
		}
		if !allowed || declared.Mode != 0o644 {
			return fmt.Errorf("deployment package contains an asset outside its profile")
		}
	}
	for _, component := range components {
		for _, asset := range unitSpecs[component.ID].assets {
			found := false
			for name := range observed {
				if name == asset.destination && !asset.tree || asset.tree && strings.HasPrefix(name, asset.destination+"/") && unitAssetExtension(name) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("deployment package is missing assets for %s", component.ID)
			}
			if asset.tree && strings.HasSuffix(asset.source, "/config") {
				required := []string{"app.yaml", "trpc_go.yaml"}
				if component.ID == "console-proxy" {
					required = []string{"app.yaml"}
				}
				if component.ID == "admin" {
					required = append(required, "console.yaml")
				}
				for _, name := range required {
					if _, exists := observed[asset.destination+"/"+name]; !exists {
						return fmt.Errorf("deployment package is missing configuration %s/%s", component.ID, name)
					}
				}
			}
		}
	}
	return nil
}
