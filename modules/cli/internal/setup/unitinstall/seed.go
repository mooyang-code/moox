package unitinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

// SealStateOptions names a stopped, private staging tree. The producer must
// finish offline database initialization or consistent backup before sealing.
// Only explicit mutable directories are imported; no config or identity files
// can be substituted through this channel.
type SealStateOptions struct {
	Directory         string   `json:"directory"`
	ReleaseDirectory  string   `json:"release_directory"`
	PreviousDirectory string   `json:"previous_directory"`
	Paths             []string `json:"paths"`
}

type StateSeed struct {
	Version           int                `json:"version"`
	HostID            string             `json:"host_id"`
	DeploymentRoot    string             `json:"deployment_root"`
	UnitRoot          string             `json:"unit_root"`
	Profile           string             `json:"profile"`
	Directory         string             `json:"directory"`
	ReleaseDirectory  string             `json:"release_directory"`
	PreviousDirectory string             `json:"previous_directory"`
	Paths             []string           `json:"paths"`
	Files             []unitpackage.File `json:"files"`
}

type StateSeedReference struct {
	Directory string `json:"directory"`
	SHA256    string `json:"sha256"`
}

func stateDigest(raw []byte) string {
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func validStateReference(reference *StateSeedReference) bool {
	if reference == nil {
		return true
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(reference.SHA256, "sha256:"))
	return reference.Directory != "" && err == nil && len(digest) == 32 && reference.SHA256 == "sha256:"+hex.EncodeToString(digest)
}

func validateSeedBinding(seed StateSeed, prepared Prepared, previous string) error {
	if seed.Version != 1 || seed.HostID != prepared.HostID || seed.DeploymentRoot != prepared.DeploymentRoot || seed.UnitRoot != prepared.UnitRoot || seed.Profile != prepared.Profile || seed.ReleaseDirectory != prepared.Directory || seed.PreviousDirectory != previous {
		return errors.New("state seed does not match the target release and previous current")
	}
	if filepath.Dir(seed.Directory) != filepath.Join(prepared.UnitRoot, "state-imports") || !validReleaseID(filepath.Base(seed.Directory)) {
		return errors.New("state seed must be a physical private child of the unit state-imports directory")
	}
	allowed := mutablePaths(prepared, false)
	if len(seed.Paths) == 0 || !validSelection(seed.Paths, allowed) || !slices.IsSorted(seed.Paths) {
		return errors.New("state seed requires distinct sorted mutable directory paths")
	}
	return nil
}

// SealState binds an offline snapshot to one new release and the expected
// current. It never stops services or takes a live database file copy. Sealed
// bytes are rechecked before stopping anything and after import into candidate.
func SealState(ctx context.Context, options SealStateOptions, lock unitruntime.Options) (result StateSeedReference, returnErr error) {
	prepared, err := ReadPrepared(ctx, options.ReleaseDirectory)
	if err != nil {
		return result, err
	}
	paths := slices.Clone(options.Paths)
	slices.Sort(paths)
	seed := StateSeed{Version: 1, HostID: prepared.HostID, DeploymentRoot: prepared.DeploymentRoot, UnitRoot: prepared.UnitRoot, Profile: prepared.Profile, Directory: options.Directory, ReleaseDirectory: prepared.Directory, PreviousDirectory: options.PreviousDirectory, Paths: paths}
	if err := validateSeedBinding(seed, prepared, options.PreviousDirectory); err != nil {
		return result, err
	}
	err = unitruntime.WithMaintenance(ctx, prepared.DeploymentRoot, prepared.HostID, lock, func(_ *unitruntime.Maintenance) error {
		unit, err := fsutil.OpenPhysicalRoot(prepared.UnitRoot, false)
		if err != nil {
			return err
		}
		defer unit.Close()
		current, err := currentRelease(unit, prepared.UnitRoot)
		if err != nil {
			return err
		}
		if current != options.PreviousDirectory {
			return errors.New("state seed previous current changed before sealing")
		}
		// Recheck the candidate after acquiring the host lock.
		if _, err := ReadPrepared(ctx, prepared.Directory); err != nil {
			return err
		}
		root, err := openSeedRoot(seed.Directory)
		if err != nil {
			return err
		}
		defer root.Close()
		if err := validateSeedTree(ctx, root, seed.Paths); err != nil {
			return err
		}
		seed.Files, err = stateInventory(ctx, root, seed.Paths)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(seed)
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		if len(raw) > 1<<20 {
			return errors.New("state seed manifest exceeds its size bound")
		}
		if old, err := fsutil.ReadPrivate(root, "state-seed.json", 1<<20); err == nil {
			if !bytes.Equal(raw, old) {
				return errors.New("state seed was already sealed with different content")
			}
		} else if _, err := root.Lstat("state-seed.json"); !os.IsNotExist(err) {
			return errors.New("existing state seed receipt is invalid")
		} else {
			if err := syncTree(root); err != nil {
				return err
			}
			if err := fsutil.WritePrivate(root, "state-seed.json", raw, false); err != nil {
				return err
			}
		}
		result = StateSeedReference{Directory: seed.Directory, SHA256: stateDigest(raw)}
		return nil
	})
	return result, err
}

func openSeedRoot(directory string) (*os.Root, error) {
	parent, err := fsutil.OpenPhysicalRoot(filepath.Dir(directory), true)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return fsutil.OpenPhysicalRoot(directory, true)
}

func loadStateSeed(ctx context.Context, reference *StateSeedReference, prepared Prepared, previous string) (StateSeed, error) {
	if reference == nil {
		return StateSeed{}, nil
	}
	if !validStateReference(reference) {
		return StateSeed{}, errors.New("state seed requires a producer SHA256 and directory")
	}
	if filepath.Dir(reference.Directory) != filepath.Join(prepared.UnitRoot, "state-imports") || !validReleaseID(filepath.Base(reference.Directory)) {
		return StateSeed{}, errors.New("state seed source escapes its unit staging directory")
	}
	root, err := openSeedRoot(reference.Directory)
	if err != nil {
		return StateSeed{}, err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, "state-seed.json", 1<<20)
	if err != nil {
		return StateSeed{}, err
	}
	if stateDigest(raw) != reference.SHA256 {
		return StateSeed{}, errors.New("state seed receipt does not match the producer digest")
	}
	var seed StateSeed
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&seed) != nil || decoder.Decode(new(any)) != io.EOF {
		return StateSeed{}, errors.New("state seed receipt requires one known JSON document")
	}
	canonical, err := json.Marshal(seed)
	if err != nil || !bytes.Equal(raw, append(canonical, '\n')) || seed.Directory != reference.Directory {
		return StateSeed{}, errors.New("state seed receipt must use canonical JSON and its physical directory")
	}
	if err := validateSeedBinding(seed, prepared, previous); err != nil {
		return StateSeed{}, err
	}
	if err := validateSeedTree(ctx, root, seed.Paths); err != nil {
		return StateSeed{}, err
	}
	files, err := stateInventory(ctx, root, seed.Paths)
	if err != nil {
		return StateSeed{}, err
	}
	if !reflect.DeepEqual(files, seed.Files) {
		return StateSeed{}, errors.New("state seed changed after sealing")
	}
	return seed, nil
}

func validateSeedTree(ctx context.Context, root *os.Root, paths []string) error {
	entries := 0
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > 8192 {
			return errors.New("state seed contains too many objects")
		}
		if name == "state-seed.json" {
			info, err := root.Lstat(name)
			if err != nil || !fsutil.SingleLink(info) {
				return errors.New("state seed receipt must not be hard linked")
			}
			_, err = fsutil.ReadPrivate(root, name, 1<<20)
			return err
		}
		info, err := entry.Info()
		if err != nil || !fsutil.Owned(info) {
			return errors.New("state seed ownership cannot be verified")
		}
		allowed := name == "." || slices.ContainsFunc(paths, func(p string) bool {
			return name == p || strings.HasPrefix(name, p+"/") || info.IsDir() && strings.HasPrefix(p, name+"/")
		})
		if !allowed || !(info.IsDir() && info.Mode().Perm() == 0o700 || info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && fsutil.SingleLink(info)) {
			return errors.New("state seed only accepts private physical files in declared mutable paths")
		}
		return nil
	})
}

func stateInventory(ctx context.Context, root *os.Root, paths []string) ([]unitpackage.File, error) {
	files := []unitpackage.File{}
	entries := 0
	for _, directory := range paths {
		// Check each ancestor before walking: WalkDir on a file path must not
		// accidentally traverse a substituted component/data parent symlink.
		for name := directory; name != "."; name = path.Dir(name) {
			info, err := root.Lstat(name)
			if err != nil || !info.IsDir() || !fsutil.Owned(info) || info.Mode().Perm() != 0o700 {
				return nil, errors.New("state seed directory must be owned physical 0700")
			}
		}
		err := fs.WalkDir(root.FS(), directory, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			entries++
			if entries > 8192 || len(files) > 4096 {
				return errors.New("state seed inventory exceeds its object bound")
			}
			info, err := entry.Info()
			if err != nil || !fsutil.Owned(info) {
				return errors.New("state seed object ownership cannot be verified")
			}
			if info.IsDir() && info.Mode().Perm() == 0o700 {
				return nil
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !fsutil.SingleLink(info) || len(files) >= 4096 {
				return errors.New("state seed files must be owned physical 0600")
			}
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			defer file.Close()
			opened, err := file.Stat()
			if err != nil || !os.SameFile(info, opened) || !fsutil.SingleLink(opened) {
				return errors.New("state seed file changed while opening")
			}
			hash := sha256.New()
			n, err := io.Copy(hash, &contextReader{ctx: ctx, reader: io.LimitReader(file, info.Size()+1)})
			if err != nil {
				return err
			}
			closedInfo, err := file.Stat()
			if err != nil || n != info.Size() || closedInfo.Size() != info.Size() || !closedInfo.ModTime().Equal(info.ModTime()) || !fsutil.SingleLink(closedInfo) {
				return errors.New("state seed file changed while hashing")
			}
			files = append(files, unitpackage.File{Path: name, SHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Mode: 0o600, Size: n})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func importState(ctx context.Context, seed StateSeed, candidate Prepared) error {
	if seed.Directory == "" {
		return nil
	}
	if err := copyStatePaths(ctx, seed.Directory, candidate.Directory, seed.Paths); err != nil {
		return err
	}
	root, err := fsutil.OpenPhysicalRoot(candidate.Directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	files, err := stateInventory(ctx, root, seed.Paths)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(files, seed.Files) {
		return errors.New("imported state does not match its sealed snapshot")
	}
	return nil
}
