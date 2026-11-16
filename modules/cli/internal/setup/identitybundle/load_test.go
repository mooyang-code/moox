package identitybundle

import (
	"context"
	"errors"
	"io/fs"
	"path"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

// Load 从本地私密目录读取并校验一份身份包，只给测试用：生产路径一律经 Fetch 从控制机下载。
func Load(ctx context.Context, directory string, options Options) (*Material, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, "bundle.json", maxMetadata)
	if err != nil {
		return nil, err
	}
	var metadata hostbundle.Metadata
	if err := canonical(raw, &metadata); err != nil {
		return nil, err
	}
	if err := validateMetadata(metadata, options); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	expected := map[string]bool{"bundle.json": true}
	directories := map[string]bool{".": true}
	for _, file := range metadata.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := fsutil.ReadPrivate(root, file.Path, file.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) != file.Size || digest(raw) != file.SHA256 {
			return nil, errors.New("host material file digest mismatch")
		}
		files[file.Path] = raw
		expected[file.Path] = true
		for dir := path.Dir(file.Path); dir != "."; dir = path.Dir(dir) {
			directories[dir] = true
		}
	}
	if err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() && directories[name] && info.Mode().Perm() == 0o700 && fsutil.Owned(info) {
			return nil
		}
		if !entry.IsDir() && expected[name] && info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && fsutil.Owned(info) {
			return nil
		}
		return errors.New("host material contains an undeclared file, link or unsafe directory")
	}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return verify(metadata, files, options)
}

// LoadEventBusClientsAt 校验已落盘的 EventBus 角色文件，只给测试用。
func LoadEventBusClientsAt(ctx context.Context, metadata hostbundle.ClientMetadata, roles []string, directory string) error {
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	_, err = readEventBusClients(ctx, metadata, roles, func(name string, max int64) ([]byte, error) { return fsutil.ReadPrivate(root, name, max) })
	return err
}
