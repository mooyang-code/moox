package identitybundle

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

// FetchEventBusClients 只取清单里列出的客户端角色文件；Server 导出另带 broker 的用户表（eventbus 组件自己用）。
// 导出里没有证书：EventBus 用所在主机的主机证书，根证书在主机身份包里。
func FetchEventBusClients(ctx context.Context, download Download, metadata hostbundle.ClientMetadata, roles []string, destination string) error {
	if download == nil {
		return errors.New("EventBus clients require verified SSH")
	}
	files, err := readEventBusClients(ctx, metadata, roles, func(name string, max int64) ([]byte, error) {
		return downloadFile(ctx, download, path.Join(metadata.OutputDir, name), max)
	})
	if err != nil {
		return err
	}
	_, err = fsutil.PublishPrivate(ctx, destination, files)
	return err
}

func readEventBusClients(ctx context.Context, metadata hostbundle.ClientMetadata, roles []string, read func(string, int64) ([]byte, error)) (map[string][]byte, error) {
	fail := func() (map[string][]byte, error) {
		return nil, errors.New("EventBus client inventory or material is invalid")
	}
	var wanted []string
	if metadata.Server {
		wanted = append(wanted, hostbundle.UsersFile)
	}
	for _, role := range roles {
		wanted = append(wanted, role+".yaml")
	}
	if metadata.Version != 1 || metadata.Status != "ok" || !path.IsAbs(metadata.OutputDir) || path.Clean(metadata.OutputDir) != metadata.OutputDir || len(roles) == 0 || len(roles) > 16 || !slices.Equal(metadata.Roles, roles) || !slices.IsSorted(roles) || len(slices.Compact(slices.Clone(roles))) != len(roles) || len(metadata.Files) != len(wanted) {
		return fail()
	}
	for _, role := range roles {
		if role == "" || len(role) > 64 || strings.ContainsFunc(role, func(r rune) bool { return r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) {
			return fail()
		}
	}
	slices.Sort(wanted)
	files := map[string][]byte{}
	for i, item := range metadata.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.Path != wanted[i] || item.Size <= 0 || item.Size > 64<<10 {
			return fail()
		}
		raw, err := read(item.Path, item.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) != item.Size || digest(raw) != item.SHA256 {
			return fail()
		}
		files[item.Path] = raw
	}
	raw, err := read("clients.json", 128<<10)
	if err != nil {
		return nil, err
	}
	var stored hostbundle.ClientMetadata
	if canonical(raw, &stored) != nil {
		return fail()
	}
	expected, _ := json.Marshal(metadata)
	actual, _ := json.Marshal(stored)
	if string(expected) != string(actual) {
		return fail()
	}
	files["clients.json"] = raw
	return files, nil
}
