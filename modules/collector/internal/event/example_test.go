package event

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/pkg/logger"
)

// TestNotifierExample 通知器使用示例
func TestNotifierExample(t *testing.T) {
	// 创建通知器
	notifier := NewNotifier(DefaultConfig, logger.NewDefault())
	
	// 启动通知器
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := notifier.Start(ctx); err != nil {
		t.Fatalf("启动通知器失败: %v", err)
	}
	defer notifier.Stop(ctx)
	
	// 创建接收通道来验证通知
	received := make(chan Notification, 1)
	
	// 订阅通知
	handler := func(notification Notification) {
		received <- notification
	}
	
	if err := notifier.Subscribe("test.event", handler); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	
	// 发布通知
	testData := map[string]interface{}{
		"message": "Hello, Notifier!",
		"value":   42,
	}
	
	if err := notifier.Publish("test.event", testData); err != nil {
		t.Fatalf("发布通知失败: %v", err)
	}
	
	// 等待并验证通知
	select {
	case notification := <-received:
		if notification.Type != "test.event" {
			t.Errorf("期望事件类型 'test.event', 得到 %s", notification.Type)
		}
		
		data, ok := notification.Data.(map[string]interface{})
		if !ok {
			t.Errorf("期望数据为 map[string]interface{}, 得到 %T", notification.Data)
		}
		
		if data["message"] != "Hello, Notifier!" {
			t.Errorf("期望消息 'Hello, Notifier!', 得到 %v", data["message"])
		}
		
		t.Logf("成功接收通知: %+v", notification)
		
	case <-time.After(2 * time.Second):
		t.Error("超时未收到通知")
	}
	
	// 验证统计信息
	stats := notifier.GetStats()
	if stats.PublishedTotal != 1 {
		t.Errorf("期望发布总数为 1, 得到 %d", stats.PublishedTotal)
	}
	
	if stats.ProcessedTotal != 1 {
		t.Errorf("期望处理总数为 1, 得到 %d", stats.ProcessedTotal)
	}
	
	t.Logf("通知器统计信息: %+v", stats)
}

// TestNotificationTypes 测试预定义通知类型常量
func TestNotificationTypes(t *testing.T) {
	// 验证预定义的通知类型常量
	expectedTypes := []string{
		NotificationTypeTaskCreated,
		NotificationTypeTaskUpdated,
		NotificationTypeTaskDeleted,
		NotificationTypeTaskStarted,
		NotificationTypeTaskCompleted,
		NotificationTypeTaskFailed,
		NotificationTypeConfigUpdated,
		NotificationTypeConfigSynced,
		NotificationTypeHeartbeatSent,
		NotificationTypeHeartbeatFailed,
		NotificationTypeCollectorStarted,
		NotificationTypeCollectorStopped,
		NotificationTypeCollectorFailed,
		NotificationTypeDataCollected,
		NotificationTypeDataStored,
	}
	
	for _, notificationType := range expectedTypes {
		if notificationType == "" {
			t.Errorf("通知类型不应为空")
		}
		t.Logf("通知类型: %s", notificationType)
	}
}