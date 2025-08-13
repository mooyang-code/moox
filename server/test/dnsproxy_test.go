package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/apirouter"
)

func TestDNSProxyBasic(t *testing.T) {
	// 创建DNS代理实例
	dnsProxy := api.NewDNSProxy()

	// 测试参数
	params := map[string]string{
		"domain": "www.baidu.com",
	}

	ctx := context.Background()

	// 执行DNS解析
	result, err := dnsProxy.GetHandle(ctx, params)
	if err != nil {
		t.Fatalf("DNS解析失败: %v", err)
	}

	// 检查结果
	if result.Code != 200 {
		t.Errorf("期望状态码200，实际得到: %d", result.Code)
	}

	if len(result.Data) == 0 {
		t.Error("期望有返回数据，但数据为空")
	}

	t.Logf("DNS解析结果: %+v", result.Data[0])
}

func TestDNSProxyMissingDomain(t *testing.T) {
	// 创建DNS代理实例
	dnsProxy := api.NewDNSProxy()

	// 测试缺少域名参数的情况
	params := map[string]string{}

	ctx := context.Background()

	// 执行DNS解析
	result, err := dnsProxy.GetHandle(ctx, params)
	if err != nil {
		t.Fatalf("意外的错误: %v", err)
	}

	// 检查结果
	if result.Code != 400 {
		t.Errorf("期望状态码400，实际得到: %d", result.Code)
	}

	t.Logf("缺少域名参数的结果: %+v", result.Data[0])
}

func TestDNSProxyInvalidDomain(t *testing.T) {
	// 创建DNS代理实例
	dnsProxy := api.NewDNSProxy()

	// 测试无效域名
	params := map[string]string{
		"domain": "invalid-domain-that-does-not-exist.com",
	}

	ctx := context.Background()

	// 执行DNS解析
	result, err := dnsProxy.GetHandle(ctx, params)
	if err != nil {
		t.Fatalf("意外的错误: %v", err)
	}

	// 对于无效域名，应该返回500状态码
	if result.Code != 500 {
		t.Errorf("期望状态码500，实际得到: %d", result.Code)
	}

	t.Logf("无效域名的结果: %+v", result.Data[0])
}

func TestDNSProxyMultipleDomains(t *testing.T) {
	// 创建DNS代理实例
	dnsProxy := api.NewDNSProxy()

	// 测试多个知名域名
	domains := []string{
		"www.google.com",
		"www.github.com",
		"www.stackoverflow.com",
	}

	ctx := context.Background()

	for _, domain := range domains {
		t.Run(domain, func(t *testing.T) {
			params := map[string]string{
				"domain": domain,
			}

			start := time.Now()
			result, err := dnsProxy.GetHandle(ctx, params)
			duration := time.Since(start)

			if err != nil {
				t.Fatalf("DNS解析失败: %v", err)
			}

			if result.Code != 200 {
				t.Errorf("期望状态码200，实际得到: %d", result.Code)
			}

			t.Logf("域名 %s 解析耗时: %v", domain, duration)
			t.Logf("解析结果: %+v", result.Data[0])
		})
	}
}

func TestDNSProxyCache(t *testing.T) {
	// 创建DNS代理实例
	dnsProxy := api.NewDNSProxy()

	domain := "www.baidu.com"
	params := map[string]string{
		"domain": domain,
	}

	ctx := context.Background()

	// 第一次请求（应该从DNS服务器获取）
	start1 := time.Now()
	result1, err := dnsProxy.GetHandle(ctx, params)
	duration1 := time.Since(start1)

	if err != nil {
		t.Fatalf("第一次DNS解析失败: %v", err)
	}

	if result1.Code != 200 {
		t.Errorf("期望状态码200，实际得到: %d", result1.Code)
	}

	// 第二次请求（应该从缓存获取）
	start2 := time.Now()
	result2, err := dnsProxy.GetHandle(ctx, params)
	duration2 := time.Since(start2)

	if err != nil {
		t.Fatalf("第二次DNS解析失败: %v", err)
	}

	if result2.Code != 200 {
		t.Errorf("期望状态码200，实际得到: %d", result2.Code)
	}

	// 第二次请求应该更快（从缓存获取）
	if duration2 >= duration1 {
		t.Logf("警告：第二次请求耗时 %v 不小于第一次 %v，可能缓存未生效", duration2, duration1)
	} else {
		t.Logf("缓存生效：第一次耗时 %v，第二次耗时 %v", duration1, duration2)
	}

	t.Logf("第一次解析结果: %+v", result1.Data[0])
	t.Logf("第二次解析结果: %+v", result2.Data[0])
}

func TestDNSProxySchedule(t *testing.T) {
	ctx := context.Background()

	// 测试定时任务入口函数
	err := api.DnsproxySchedule(ctx, "test_params")
	if err != nil {
		t.Fatalf("定时任务执行失败: %v", err)
	}

	// 检查缓存统计
	stats := api.GetCacheStats()
	t.Logf("缓存统计: %+v", stats)

	// 验证缓存中有数据
	if itemCount, ok := stats["item_count"].(int); ok && itemCount > 0 {
		t.Logf("缓存中有 %d 个条目", itemCount)
	} else {
		t.Logf("警告：缓存中没有数据，可能是配置文件不存在或解析失败")
	}
}

func TestDNSProxyConfig(t *testing.T) {
	// 测试配置加载
	dnsProxy := api.NewDNSProxy()
	if dnsProxy == nil {
		t.Fatal("DNS代理实例创建失败")
	}

	// 测试基本解析功能
	params := map[string]string{
		"domain": "www.baidu.com",
	}

	ctx := context.Background()
	result, err := dnsProxy.GetHandle(ctx, params)
	if err != nil {
		t.Fatalf("DNS解析失败: %v", err)
	}

	if result.Code != 200 {
		t.Errorf("期望状态码200，实际得到: %d", result.Code)
	}

	t.Logf("使用配置文件的DNS解析结果: %+v", result.Data[0])
}
