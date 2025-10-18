# MooX 文件下载安全配置指南

## 概述

MooX 文件服务器已经实现了完整的JWT鉴权机制，确保只有授权用户才能访问文件下载URL。

## 安全特性

### ✅ 已实现的安全功能

1. **JWT令牌鉴权**
   - 用户请求下载URL时自动生成包含用户ID和文件路径的JWT令牌
   - 令牌有效期30分钟，防止长期滥用
   - 文件访问时严格验证令牌的合法性和文件路径匹配

2. **路径安全防护** 
   - 防止路径遍历攻击（..、~等危险字符）
   - 只允许下载指定扩展名的文件（.zip、.tar.gz等）
   - 确保文件路径在允许的目录内

3. **访问日志记录**
   - 记录每次文件访问的用户ID、IP地址、访问时间
   - 详细的安全事件日志（非法路径访问、令牌验证失败等）

4. **安全响应头**
   - X-Content-Type-Options: nosniff
   - X-Frame-Options: DENY  
   - X-XSS-Protection: 1; mode=block
   - Strict-Transport-Security
   - Content-Security-Policy

## 环境变量配置

### JWT密钥配置（生产环境必须设置）

```bash
# JWT签名密钥（生产环境必须设置为强密钥）
export MOOX_JWT_SECRET_KEY="your-super-secret-key-here"

# JWT颁发者标识（可选）
export MOOX_JWT_ISSUER="your-company-moox-server"
```

### 推荐的生产环境密钥生成

```bash
# 生成强随机密钥
openssl rand -base64 64
# 或
uuidgen && uuidgen && uuidgen | tr -d '\n'
```

## API使用流程

### 1. 前端请求下载URL

```http
GET /api/v1/function-packages/123/download-url
Authorization: Bearer <用户的访问令牌>
```

### 2. 后台生成带JWT的下载URL

```json
{
  "code": 200,
  "message": "获取下载URL成功",
  "data": {
    "id": 123,
    "package_name": "my-package",
    "version": "1.0.0", 
    "filename": "my-package_1.0.0.zip",
    "download_url": "/files/packages/my-package_1.0.0.zip?token=eyJhbGciOiJIUzI1NiIs...",
    "file_size": 1024000,
    "file_md5": "abcd1234..."
  }
}
```

### 3. 用户访问下载URL

- URL中的token参数包含用户身份和文件路径信息
- 文件服务器验证token的有效性、过期时间、文件路径匹配
- 只有验证通过的请求才能下载文件

## 安全日志示例

```
[INFO] [FileServer] JWT验证成功，用户: user123, 文件: packages/app_1.0.0.zip (IP: 192.168.1.100)
[INFO] [FileServer] 文件下载开始 - 用户: user123, 文件: packages/app_1.0.0.zip, 大小: 1024000 bytes, IP: 192.168.1.100
[WARN] [FileServer] JWT验证失败: token解析失败 (IP: 192.168.1.101, Path: packages/app_1.0.0.zip)  
[WARN] [FileServer] 检测到可疑的文件路径: ../../../etc/passwd (IP: 192.168.1.102)
```

## 安全最佳实践

### 生产环境配置

1. **设置强JWT密钥**
   ```bash
   export MOOX_JWT_SECRET_KEY="$(openssl rand -base64 64)"
   ```

2. **启用HTTPS**
   ```bash
   # 在反向代理(如nginx)中配置SSL证书
   server {
       listen 443 ssl;
       ssl_certificate /path/to/cert.pem;
       ssl_certificate_key /path/to/key.pem;
   }
   ```

3. **配置访问控制**
   ```bash
   # 限制文件服务器只能从内网访问
   # 通过防火墙或nginx配置IP白名单
   ```

### 监控告警

建议监控以下安全事件：
- JWT验证失败次数过多
- 路径遍历攻击尝试  
- 异常IP访问模式
- 大量文件下载请求

## 故障排除

### 常见错误码

| 错误码 | 说明 | 解决方案 |
|--------|------|----------|
| MISSING_TOKEN | 缺少访问令牌 | 确保URL包含token参数 |
| INVALID_TOKEN | 令牌无效或过期 | 重新获取下载URL |
| ILLEGAL_PATH | 非法文件路径 | 检查文件路径是否包含危险字符 |
| FILE_NOT_FOUND | 文件不存在 | 确认文件已正确上传到服务器 |

### 调试方法

```bash
# 查看JWT令牌内容（用于调试）
echo "eyJhbGciOiJIUzI1NiIs..." | cut -d. -f2 | base64 -d | jq

# 检查文件服务器日志
tail -f /path/to/moox/logs/file-server.log | grep "FileServer"
```

## 总结

当前的实现已经提供了企业级的文件下载安全保护：

✅ **JWT令牌鉴权** - 防止未授权访问  
✅ **路径安全检查** - 防止路径遍历攻击  
✅ **访问日志记录** - 便于安全审计  
✅ **安全响应头** - 防止各类Web攻击  
✅ **配置化密钥** - 支持生产环境部署  

你的文件下载服务现在是安全的！🛡️
