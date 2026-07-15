package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

const maxRouteFileBytes = 16 << 20

type Routes struct{ directory string }

func NewRoutes(directory string) *Routes { return &Routes{directory: directory} }

func (routes *Routes) Path() string { return filepath.Join(routes.directory, "routes.json") }

func (routes *Routes) Save(snapshot gatewayproxy.Snapshot) (resultErr error) {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	if err := ensureSecureRouteDirectory(routes.directory, true); err != nil {
		return err
	}
	if err := ensureSecureCacheFile(routes.Path(), true); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode routes: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(routes.directory, ".routes-*.tmp")
	if err != nil {
		return fmt.Errorf("create route temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure route temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("write route temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync route temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close route temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, routes.Path()); err != nil {
		return fmt.Errorf("replace route file: %w", err)
	}
	directory, err := os.Open(routes.directory)
	if err != nil {
		return fmt.Errorf("open route directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync route directory: %w", err)
	}
	return nil
}

func (routes *Routes) Load() (gatewayproxy.Snapshot, error) {
	if err := ensureSecureRouteDirectory(routes.directory, false); err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	if err := ensureSecureCacheFile(routes.Path(), false); err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	file, err := os.Open(routes.Path())
	if err != nil {
		return gatewayproxy.Snapshot{}, fmt.Errorf("open routes: %w", err)
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxRouteFileBytes+1))
	if err != nil {
		return gatewayproxy.Snapshot{}, fmt.Errorf("read routes: %w", err)
	}
	if len(encoded) > maxRouteFileBytes {
		return gatewayproxy.Snapshot{}, errors.New("route file is too large")
	}
	var snapshot gatewayproxy.Snapshot
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return gatewayproxy.Snapshot{}, fmt.Errorf("decode routes: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return gatewayproxy.Snapshot{}, errors.New("route file contains trailing JSON")
	}
	if err := validateSnapshot(snapshot); err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	return snapshot, nil
}

func ensureSecureRouteDirectory(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create route store: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect route store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("route store must be a real directory, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("route store permissions must be owner-only (got %04o)", info.Mode().Perm())
	}
	return nil
}

func ensureSecureCacheFile(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect route cache: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("route cache must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("route cache permissions must be owner-only (got %04o)", info.Mode().Perm())
	}
	return nil
}

func validateSnapshot(snapshot gatewayproxy.Snapshot) error {
	if snapshot.NodeID == "" {
		return errors.New("route snapshot node_id is required")
	}
	var table gatewayproxy.Table
	if err := table.Replace(snapshot); err != nil {
		return fmt.Errorf("validate route snapshot: %w", err)
	}
	return nil
}
