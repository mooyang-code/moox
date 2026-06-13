package logic

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
)

// MetaServicerImpl 元数据服务实现结构体
type MetaServicerImpl struct {
	dbDAO dao.DataInterfacer // 读写DB接口
}

// NewMetaServicerImpl 新建元数据字段服务实现
func NewMetaServicerImpl() (*MetaServicerImpl, error) {
	var imp MetaServicerImpl
	var err error
	imp.dbDAO, err = dao.NewDataInterfacer()
	if err != nil {
		return nil, err
	}
	// 初始化元数据(写入系统默认配置)
	err = imp.dbDAO.InitMetadata()
	if err != nil {
		return nil, err
	}
	return &imp, nil
}
