package unitinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

// ResealResult 列出已安装发布的文件清单与实际文件的差异（只有路径，不含内容）。
type ResealResult struct {
	Unit      string   `json:"unit"`
	Release   string   `json:"release"`
	Added     []string `json:"added,omitempty"`
	Changed   []string `json:"changed,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Unchanged int      `json:"unchanged"`
	Resealed  bool     `json:"resealed"`
	// Backup 是改写前的清单副本（放在单元根目录 reseal-backups/ 下，不在发布目录里）。
	Backup string `json:"backup,omitempty"`
}

// Reseal 把单元当前发布的文件清单（prepared.json）重新封存为现状。
// 用于发布被原地换过二进制或改过配置（例如 native-release）之后，让单元工具重新接受它；write 为 false 时只列出差异。
// 除文件清单外的收据字段（主机、单元、组件、软件摘要）照常校验，不会被改写。
func Reseal(ctx context.Context, unitRoot string, write bool, lockOptions unitruntime.Options, now func() time.Time) (ResealResult, error) {
	result := ResealResult{Unit: unitRoot}
	if now == nil {
		now = time.Now
	}
	current, err := filepath.EvalSymlinks(filepath.Join(unitRoot, "current"))
	if err != nil {
		return result, errors.New("unit has no current release")
	}
	if filepath.Dir(current) != filepath.Join(unitRoot, "releases") {
		return result, errors.New("current release is not under the unit's releases directory")
	}
	result.Release = filepath.Base(current)
	prepared, files, err := currentInventory(ctx, current)
	if err != nil {
		return result, err
	}
	diffInventory(prepared.Files, files, &result)
	if !write || (len(result.Added) == 0 && len(result.Changed) == 0 && len(result.Removed) == 0) {
		return result, nil
	}
	err = unitruntime.WithMaintenance(ctx, prepared.DeploymentRoot, prepared.HostID, lockOptions, func(*unitruntime.Maintenance) error {
		// 持锁后重新计算，确保写入的就是锁内看到的现状。
		locked, lockedFiles, err := currentInventory(ctx, current)
		if err != nil {
			return err
		}
		var again ResealResult
		diffInventory(locked.Files, lockedFiles, &again)
		if !slices.Equal(again.Added, result.Added) || !slices.Equal(again.Changed, result.Changed) || !slices.Equal(again.Removed, result.Removed) {
			return errors.New("release files changed while resealing; rerun to review the new difference")
		}
		old, err := json.Marshal(locked)
		if err != nil {
			return err
		}
		unit, err := fsutil.OpenPhysicalRoot(unitRoot, true)
		if err != nil {
			return err
		}
		defer unit.Close()
		if err := unit.MkdirAll("reseal-backups", 0o700); err != nil {
			return err
		}
		backup := filepath.Join("reseal-backups", fmt.Sprintf("%s-%s.json", result.Release, now().UTC().Format("20060102T150405Z")))
		if err := fsutil.WritePrivate(unit, backup, append(old, '\n'), false); err != nil {
			return err
		}
		locked.Files = lockedFiles
		raw, err := json.Marshal(locked)
		if err != nil {
			return err
		}
		release, err := fsutil.OpenPhysicalRoot(current, true)
		if err != nil {
			return err
		}
		defer release.Close()
		if err := fsutil.WritePrivate(release, "prepared.json", append(raw, '\n'), true); err != nil {
			return err
		}
		if _, err := ReadInstalled(ctx, current); err != nil {
			return fmt.Errorf("resealed release still fails verification: %w", err)
		}
		result.Backup = filepath.Join(unitRoot, backup)
		return nil
	})
	if err != nil {
		return result, err
	}
	result.Resealed = true
	return result, nil
}

// currentInventory 按已安装发布的规则校验收据（不核对文件清单），并算出当前实际的文件清单。
func currentInventory(ctx context.Context, directory string) (Prepared, []unitpackage.File, error) {
	prepared, err := readRelease(ctx, directory, true, false)
	if err != nil {
		return Prepared{}, nil, err
	}
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return Prepared{}, nil, err
	}
	defer root.Close()
	files, err := inventory(ctx, root, prepared, true)
	if err != nil {
		return Prepared{}, nil, err
	}
	if len(files) == 0 {
		return Prepared{}, nil, errors.New("release has no files to seal")
	}
	return prepared, files, nil
}

func diffInventory(sealed, actual []unitpackage.File, result *ResealResult) {
	old := make(map[string]unitpackage.File, len(sealed))
	for _, f := range sealed {
		old[f.Path] = f
	}
	seen := make(map[string]bool, len(actual))
	for _, f := range actual {
		seen[f.Path] = true
		previous, ok := old[f.Path]
		switch {
		case !ok:
			result.Added = append(result.Added, f.Path)
		case previous != f:
			result.Changed = append(result.Changed, f.Path)
		default:
			result.Unchanged++
		}
	}
	for _, f := range sealed {
		if !seen[f.Path] {
			result.Removed = append(result.Removed, f.Path)
		}
	}
	slices.Sort(result.Added)
	slices.Sort(result.Changed)
	slices.Sort(result.Removed)
}
