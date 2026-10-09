package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/packages/security"
	"google.golang.org/protobuf/proto"
)

// Snapshots persist route, directory and verifier keys in one private file.
type Snapshots struct{ directory, hostID string }

func NewSnapshots(directory, hostID string) *Snapshots {
	return &Snapshots{directory: directory, hostID: hostID}
}
func (s *Snapshots) Path() string { return filepath.Join(s.directory, "snapshot.pb") }

func (s *Snapshots) Prepare() error {
	root, err := s.open(true)
	if err != nil {
		return err
	}
	return root.Close()
}

func (s *Snapshots) open(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(s.directory, 0o700); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(s.directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !privateDirectory(info) {
		return nil, errors.New("snapshot store must be a regular private directory")
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("snapshot directory changed while opening")
	}
	return root, nil
}

func (s *Snapshots) Save(view *snapshot.View) error {
	if view == nil || view.HostID() != s.hostID {
		return errors.New("snapshot cache host mismatch")
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(view.Proto())
	if err != nil || len(encoded) > snapshot.MaxBytes {
		return errors.New("snapshot cache encoding failed or too large")
	}
	root, err := s.open(true)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat("snapshot.pb"); err == nil {
		if !info.Mode().IsRegular() || !privateFile(info) {
			return errors.New("snapshot cache must be a regular private file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	id, err := security.RandomHex(16)
	if err != nil {
		return err
	}
	name := ".snapshot-" + id
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(name, "snapshot.pb"); err != nil {
		return err
	}
	return syncDirectory(root)
}

func (s *Snapshots) Load() (*snapshot.View, error) {
	root, err := s.open(false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("snapshot.pb")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !privateFile(info) {
		return nil, errors.New("snapshot cache must be a regular private file")
	}
	file, err := root.Open("snapshot.pb")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !privateFile(opened) {
		return nil, errors.New("snapshot file changed while opening")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, snapshot.MaxBytes+1))
	if err != nil || len(encoded) > snapshot.MaxBytes {
		return nil, errors.New("snapshot cache cannot be read or exceeds limit")
	}
	raw := &pb.HostGatewaySnapshot{}
	if err := proto.Unmarshal(encoded, raw); err != nil {
		return nil, errors.New("invalid snapshot cache encoding")
	}
	return snapshot.Build(s.hostID, raw)
}

func (s *Snapshots) Check() error {
	_, err := s.Load()
	return err
}
