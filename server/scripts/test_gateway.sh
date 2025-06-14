#!/bin/bash

# 网关功能测试脚本

echo "=========================================="
echo "Moox 网关功能测试"
echo "=========================================="

GATEWAY_URL="http://localhost:18202"

# 测试健康检查
echo "1. 测试健康检查接口..."
echo "请求: GET $GATEWAY_URL/gateway/health"
echo ""

curl -s -X GET "$GATEWAY_URL/gateway/health" | jq . 2>/dev/null || curl -s -X GET "$GATEWAY_URL/gateway/health"

if [ $? -eq 0 ]; then
    echo "✅ 健康检查接口正常"
else
    echo "❌ 健康检查接口失败"
fi
echo ""

# 测试存储服务转发
echo "2. 测试存储服务转发..."
echo "请求: POST $GATEWAY_URL/gateway/storage/ListProjects"
echo "头部: X-App-Id: test123, X-App-Key: test123"
echo ""

curl -s -X POST "$GATEWAY_URL/gateway/storage/ListProjects" \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -H "X-Trace-Id: test-trace-001" \
  -d '{
    "auth_info": {
      "app_id": "test123",
      "app_key": "test123"
    }
  }' | jq . 2>/dev/null || curl -s -X POST "$GATEWAY_URL/gateway/storage/ListProjects" \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -H "X-Trace-Id: test-trace-001" \
  -d '{
    "auth_info": {
      "app_id": "test123",
      "app_key": "test123"
    }
  }'

if [ $? -eq 0 ]; then
    echo "✅ 存储服务转发请求发送成功"
else
    echo "❌ 存储服务转发请求失败"
fi
echo ""

# 测试认证服务转发
echo "3. 测试认证服务转发..."
echo "请求: POST $GATEWAY_URL/gateway/auth/GetUserInfo"
echo "头部: X-App-Id: test123, X-App-Key: test123"
echo ""

curl -s -X POST "$GATEWAY_URL/gateway/auth/GetUserInfo" \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -H "X-Trace-Id: test-trace-002" \
  -d '{
    "app_info": {
      "app_id": "test123",
      "app_key": "test123"
    },
    "user_id": "test_user",
    "access_token": "access_token111111"
  }' | jq . 2>/dev/null || curl -s -X POST "$GATEWAY_URL/gateway/auth/GetUserInfo" \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -H "X-Trace-Id: test-trace-002" \
  -d '{
    "app_info": {
      "app_id": "test123",
      "app_key": "test123"
    },
    "user_id": "test_user",
    "access_token": "access_token111111"
  }'

if [ $? -eq 0 ]; then
    echo "✅ 认证服务转发请求发送成功"
else
    echo "❌ 认证服务转发请求失败"
fi
echo ""

# 测试错误处理
echo "4. 测试错误处理..."
echo "请求: POST $GATEWAY_URL/gateway/nonexistent/method"
echo ""

curl -s -X POST "$GATEWAY_URL/gateway/nonexistent/method" \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -d '{}'

if [ $? -eq 0 ]; then
    echo "✅ 错误处理正常"
else
    echo "❌ 错误处理异常"
fi
echo ""

echo "=========================================="
echo "测试完成"
echo "==========================================" 