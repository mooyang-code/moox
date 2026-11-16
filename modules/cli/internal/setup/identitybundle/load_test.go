package identitybundle

import (
	"context"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

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
