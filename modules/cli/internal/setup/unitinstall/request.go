package unitinstall

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
)

// ReadPrepareRequest reads a generated 0600 JSON request. Its map values may
// contain private environment material, so parse errors never echo raw input.
func ReadPrepareRequest(filename string) (PrepareOptions, error) {
	var options PrepareOptions
	err := readGeneratedRequest(filename, &options)
	return options, err
}

func ReadSealStateRequest(filename string) (SealStateOptions, error) {
	var options SealStateOptions
	err := readGeneratedRequest(filename, &options)
	return options, err
}

func readGeneratedRequest(filename string, options any) error {
	parent, err := fsutil.OpenPhysicalRoot(filepath.Dir(filename), false)
	if err != nil {
		return err
	}
	defer parent.Close()
	raw, err := fsutil.ReadPrivate(parent, filepath.Base(filename), 4<<20)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(options) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("installation requires one generated JSON request with known fields")
	}
	encoded, err := json.Marshal(options)
	if err != nil || !bytes.Equal(raw, append(encoded, '\n')) {
		return errors.New("installation request must use the producer's canonical JSON encoding")
	}
	return nil
}
