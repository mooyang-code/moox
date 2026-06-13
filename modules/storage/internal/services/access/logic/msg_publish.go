package logic

import (
	"context"
	"encoding/json"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"trpc.group/trpc-go/trpc-go/log"
)

// MessageInfo 消息相关信息接口
type MessageInfo interface {
	// GetID 获取消息ID
	GetID() string
	// SetID 设置消息ID
	SetID(id string)
}

// DataDetailModifyMsgAdapter 数据详情变更消息适配器
type DataDetailModifyMsgAdapter struct {
	Msg *pb.DataDetailModifyMsg
}

// GetID 获取数据详情变更消息ID
func (a *DataDetailModifyMsgAdapter) GetID() string {
	return a.Msg.MsgId
}

// SetID 设置数据详情变更消息ID
func (a *DataDetailModifyMsgAdapter) SetID(id string) {
	a.Msg.MsgId = id
}

// ObjectModifyMsgAdapter 对象变更消息适配器
type ObjectModifyMsgAdapter struct {
	Msg *pb.ObjectModifyMsg
}

// GetID 获取对象变更消息ID
func (a *ObjectModifyMsgAdapter) GetID() string {
	return a.Msg.MsgId
}

// SetID 设置对象变更消息ID
func (a *ObjectModifyMsgAdapter) SetID(id string) {
	a.Msg.MsgId = id
}

// PublishDataDetailChange 发布数据详情变更消息
func (i *accessorImpl) PublishDataDetailChange(ctx context.Context, msg *pb.DataDetailModifyMsg) error {
	// 验证消息有效性
	if msg == nil {
		return fmt.Errorf("无效的数据详情变更消息: 消息为空")
	}

	adapter := &DataDetailModifyMsgAdapter{Msg: msg}
	return i.publishMessage(ctx, adapter, msg, i.cfg.MsgSvrConf.DataDetailModifySubject)
}

// PublishObjectChange 发布对象变更消息
func (i *accessorImpl) PublishObjectChange(ctx context.Context, msg *pb.ObjectModifyMsg) error {
	// 验证消息有效性
	if msg == nil {
		return fmt.Errorf("无效的对象变更消息: 消息为空")
	}

	adapter := &ObjectModifyMsgAdapter{Msg: msg}
	return i.publishMessage(ctx, adapter, msg, i.cfg.MsgSvrConf.ObjectModifySubject)
}

// publishMessage 通用消息发布函数
func (i *accessorImpl) publishMessage(ctx context.Context, msgInfo MessageInfo, msg any, subject string) error {
	// 检查发布器是否已连接
	if i.publisher == nil || !i.publisher.IsConnected() {
		return fmt.Errorf("消息发布器未初始化或未连接")
	}

	// 设置消息ID
	if msgInfo.GetID() == "" {
		msgInfo.SetID(GenerateMsgID())
	}

	// 序列化消息
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %v", err)
	}

	// 发布消息
	msgSeqID, err := i.publisher.Publish(ctx, subject, data)
	if err != nil {
		log.ErrorContextf(ctx, "发布消息失败: %v", err)
		return fmt.Errorf("发布消息失败: %v", err)
	}
	log.DebugContextf(ctx, "消息发布成功: 序列号=%s, 主题=%s", msgSeqID, subject)
	return nil
}

// GenerateMsgID 使用 xid 生成唯一 消息ID(20 个字符长度的字符串)
func GenerateMsgID() string {
	return xid.New().String()
}
