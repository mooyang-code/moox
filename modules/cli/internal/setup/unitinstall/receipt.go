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
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// ReadPrepared verifies a pristine, not-yet-started release. The inventory binds
// software, rendered configuration, identities, environment and scripts to the
// receipt. Runtime state must be copied only after this check and while holding
// maintenance; an already running release is not a new preparation candidate.
func ReadPrepared(ctx context.Context, directory string) (Prepared, error) {
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return Prepared{}, err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, "prepared.json", 1<<20)
	if err != nil {
		return Prepared{}, err
	}
	var prepared Prepared
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&prepared) != nil || decoder.Decode(new(any)) != io.EOF {
		return Prepared{}, errors.New("preparation receipt requires one document with known fields")
	}
	canonical, err := json.Marshal(prepared)
	if err != nil || !bytes.Equal(raw, append(canonical, '\n')) {
		return Prepared{}, errors.New("preparation receipt must use canonical JSON")
	}
	if prepared.Version != 1 || !servicecatalog.ValidHostID(prepared.HostID) || prepared.Identity.HostID != prepared.HostID || prepared.Directory != directory || filepath.Dir(directory) != filepath.Join(prepared.UnitRoot, "releases") || !validReleaseID(filepath.Base(directory)) {
		return Prepared{}, errors.New("preparation receipt does not belong to this physical release")
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(prepared.PackageSHA256, "sha256:"))
	if err != nil || len(digest) != 32 || prepared.PackageSHA256 != "sha256:"+hex.EncodeToString(digest) {
		return Prepared{}, errors.New("preparation receipt requires a canonical software digest")
	}
	available, err := unitpackage.Components(prepared.Profile)
	if err != nil || len(prepared.Components) == 0 || len(prepared.Components) > len(available) {
		return Prepared{}, errors.New("preparation receipt requires a valid unit selection")
	}
	seen := map[string]bool{}
	for _, id := range prepared.Components {
		if seen[id] || !slices.ContainsFunc(available, func(c unitpackage.Component) bool { return c.ID == id }) {
			return Prepared{}, errors.New("preparation receipt contains an unrelated or duplicate component")
		}
		seen[id] = true
	}
	if prepared.Profile == "host" {
		if prepared.HostUnitRoot != prepared.UnitRoot || !slices.Equal(prepared.Components, []string{"host-gateway", "host-agent"}) {
			return Prepared{}, errors.New("prepared host unit must contain its canonical components")
		}
	} else if err := validateHostView(PrepareOptions{HostUnitRoot: prepared.HostUnitRoot, UnitRoot: prepared.UnitRoot, DeploymentRoot: prepared.DeploymentRoot}, prepared.HostID); err != nil {
		return Prepared{}, err
	}
	plan, err := unitruntime.LoadPlan(filepath.Join(directory, "runtime.json"))
	if err != nil {
		return Prepared{}, err
	}
	if plan.HostID != prepared.HostID || plan.DeploymentRoot != prepared.DeploymentRoot || len(plan.Components) != len(prepared.Components) {
		return Prepared{}, errors.New("prepared plan does not match its receipt")
	}
	for i, component := range plan.Components {
		if component.ID != prepared.Components[i] {
			return Prepared{}, errors.New("prepared component order does not match its receipt")
		}
	}
	files, err := inventory(ctx, root, prepared)
	if err != nil {
		return Prepared{}, err
	}
	if len(prepared.Files) == 0 || !reflect.DeepEqual(files, prepared.Files) {
		return Prepared{}, errors.New("prepared release inventory changed after preparation")
	}
	if err := unitruntime.ValidateRelease(filepath.Join(directory, "runtime.json")); err != nil {
		return Prepared{}, err
	}
	return prepared, nil
}

func inventory(ctx context.Context, root *os.Root, prepared Prepared) ([]unitpackage.File, error) {
	if prepared.Profile != "host" {
		info, err := root.Lstat("host-gateway")
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return nil, errors.New("prepared business unit requires its host directory view")
		}
	}
	var files []unitpackage.File
	var total int64
	entries := 0
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > 8192 {
			return errors.New("prepared release contains too many objects")
		}
		info, err := entry.Info()
		if err != nil || !fsutil.Owned(info) {
			return errors.New("prepared release object ownership cannot be verified")
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0o700 {
				return errors.New("prepared directories must be private 0700")
			}
			return nil
		}
		if prepared.Profile != "host" && name == "host-gateway" && info.Mode()&os.ModeSymlink != 0 {
			link, err := root.Readlink(name)
			if err != nil || link != filepath.Join(prepared.HostUnitRoot, "current", "host-gateway") {
				return errors.New("prepared business host view is invalid")
			}
			return nil
		}
		if !info.Mode().IsRegular() || (info.Mode().Perm() != 0o600 && info.Mode().Perm() != 0o644 && info.Mode().Perm() != 0o755) || info.Size() < 0 || info.Size() > 512<<20 {
			return errors.New("prepared release objects must be bounded owned regular files")
		}
		if name == "prepared.json" {
			return nil
		}
		total += info.Size()
		if total > 2<<30 || len(files) >= 4096 {
			return errors.New("prepared release exceeds its inventory bounds")
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return errors.New("prepared release file changed while opening")
		}
		hash := sha256.New()
		n, err := io.Copy(hash, &contextReader{ctx: ctx, reader: io.LimitReader(file, info.Size()+1)})
		if err != nil {
			return err
		}
		if n != info.Size() {
			return errors.New("prepared release file changed while hashing")
		}
		files = append(files, unitpackage.File{Path: name, SHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Mode: uint32(info.Mode().Perm()), Size: n})
		return nil
	})
	return files, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
