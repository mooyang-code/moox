package pebble

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	layoutMarkerName       = "storage_layout_version"
	layoutMarkerTempPrefix = "." + layoutMarkerName + ".tmp-"
	// Version 3 adds the logical time-ordered history-row index. Existing
	// stores cannot satisfy the rebuild lookback contract without that index;
	// operators must perform the explicit reset-all operation instead of
	// silently activating a View with an incomplete history.
	layoutVersion = "3\n"
)

func ensureLayout(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create DataNode store directory: %w", err)
	}

	markerPath := filepath.Join(path, layoutMarkerName)
	data, err := os.ReadFile(markerPath)
	switch {
	case err == nil:
		switch string(data) {
		case layoutVersion:
			return cleanupLayoutMarkerTemps(path)
		case "2\n":
			// Version 3 adds a derived history namespace. The legacy field
			// namespace remains readable, so upgrade the marker in place and
			// let ensureHistoryIndex lazily materialize markers per dataset.
			if err := upgradeLayoutMarker(path, markerPath); err != nil {
				return err
			}
			return cleanupLayoutMarkerTemps(path)
		default:
			return validateLayoutMarker(data)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read DataNode storage layout marker: %w", err)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect DataNode store directory: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), layoutMarkerTempPrefix) {
			if markerData, markerErr := os.ReadFile(markerPath); markerErr == nil {
				if err := validateLayoutMarker(markerData); err != nil {
					return err
				}
				return cleanupLayoutMarkerTemps(path)
			}
			return errors.New("DataNode store has data without a storage layout marker; reset DataNode store")
		}
	}
	if err := createLayoutMarker(path, markerPath); err != nil {
		return fmt.Errorf("create DataNode storage layout marker: %w", err)
	}
	return cleanupLayoutMarkerTemps(path)
}

func validateLayoutMarker(data []byte) error {
	if string(data) != layoutVersion {
		return fmt.Errorf("unsupported or damaged DataNode storage layout marker %q; reset DataNode store", string(data))
	}
	return nil
}

func cleanupLayoutMarkerTemps(dirPath string) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("inspect DataNode store directory: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), layoutMarkerTempPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dirPath, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale DataNode layout marker temp %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func createLayoutMarker(dirPath, markerPath string) error {
	temp, err := os.CreateTemp(dirPath, layoutMarkerTempPrefix)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if _, err := io.WriteString(temp, layoutVersion); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, markerPath); err != nil {
		data, readErr := os.ReadFile(markerPath)
		if readErr != nil {
			return err
		}
		if markerErr := validateLayoutMarker(data); markerErr != nil {
			return markerErr
		}
	}
	_ = os.Remove(tempPath)
	return syncDirectory(dirPath)
}

func upgradeLayoutMarker(dirPath, markerPath string) error {
	temp, err := os.CreateTemp(dirPath, layoutMarkerTempPrefix)
	if err != nil {
		return fmt.Errorf("create upgraded DataNode layout marker: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if _, err := io.WriteString(temp, layoutVersion); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, markerPath); err != nil {
		return fmt.Errorf("activate upgraded DataNode layout marker: %w", err)
	}
	return syncDirectory(dirPath)
}

func syncDirectory(dirPath string) error {
	dir, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
