/**
 * 安全加密工具模块
 * 优先使用 Web Crypto API，HTTP 环境下降级为 node-forge
 */
import forge from 'node-forge';
import { gatewayURL } from '@/api/gateway';
import { getAppInfo } from '@/api/storage/auth';

/**
 * 生成设备指纹
 */
export function generateDeviceId(): string {
    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d');
    if (ctx) {
      ctx.textBaseline = 'top';
      ctx.font = '14px Arial';
      ctx.fillText('Device fingerprint', 2, 2);
    }
    
    const fingerprint = [
      navigator.userAgent,
      navigator.language,
      screen.width + 'x' + screen.height,
      screen.colorDepth,
      new Date().getTimezoneOffset(),
      canvas.toDataURL()
    ].join('|');
    
    return btoa(fingerprint).replace(/[+/=]/g, '').substring(0, 64);
  }
  
  /**
   * 获取客户端IP地址
   */
  export function getClientIP(): string {
    return '127.0.0.1';
  }
  
  /**
   * 检查 Web Crypto API 是否可用
   */
  function isWebCryptoAvailable(): boolean {
    return typeof crypto !== 'undefined' && 
           typeof crypto.subtle !== 'undefined' && 
           typeof crypto.getRandomValues !== 'undefined';
  }

  /**
   * 从盐值和时间戳派生加密密钥材料
   */
  async function deriveKeyMaterial(salt: string, timestamp: number): Promise<ArrayBuffer> {
    const keyMaterial = salt + timestamp.toString();
    const encoder = new TextEncoder();
    const data = encoder.encode(keyMaterial);
    return await crypto.subtle.digest('SHA-256', data);
  }

  /**
   * 从盐值和时间戳派生加密密钥
   */
  async function deriveEncryptionKey(salt: string, timestamp: number): Promise<CryptoKey> {
    if (!isWebCryptoAvailable()) {
      throw new Error('Web Crypto API 不可用，请确保在 HTTPS 环境下运行');
    }

    const hashBuffer = await deriveKeyMaterial(salt, timestamp);
    
    // 导入为 AES-GCM 密钥
    return await crypto.subtle.importKey(
      'raw',
      hashBuffer,
      { name: 'AES-GCM' },
      false,
      ['encrypt']
    );
  }
  

  /**
   * node-forge 降级加密实现（真正的 AES-GCM）
   */
  function encryptPasswordWithForge(password: string, salt: string, timestamp: number): string {
    try {
      // 1. 密钥派生：与 Go 后端完全一致
      const keyMaterial = salt + timestamp.toString();
      
      // 2. SHA256 生成 32 字节密钥
      const md = forge.md.sha256.create();
      md.update(keyMaterial);
      const keyBytes = md.digest().getBytes();
      
      // 3. 生成 12 字节随机 IV（GCM 标准）
      const iv = forge.random.getBytesSync(12);
      
      // 4. 创建 AES-GCM 加密器
      const cipher = forge.cipher.createCipher('AES-GCM', keyBytes);
      cipher.start({
        iv: iv,
        tagLength: 128 // 16 字节认证标签
      });
      
      // 5. 加密数据
      cipher.update(forge.util.createBuffer(password));
      cipher.finish();
      
      const encrypted = cipher.output.getBytes();
      const tag = cipher.mode.tag.getBytes();
      
      // 6. 按照 Go AES-GCM 格式组合：iv + ciphertext + tag
      const combined = iv + encrypted + tag;
      
      // 7. Base64 编码
      const result = forge.util.encode64(combined);
      
      return result;
      
    } catch (error) {
      console.error('❌ node-forge 加密失败:', error);
      throw new Error('AES-GCM 加密失败: ' + error);
    }
  }

  /**
   * 使用 AES-GCM 加密密码（与后端协议一致）。
   */
  export async function encryptPassword(password: string, salt: string, timestamp: number): Promise<string> {
    try {
      if (isWebCryptoAvailable()) {
        // 使用 Web Crypto API
        const key = await deriveEncryptionKey(salt, timestamp);
        const encoder = new TextEncoder();
        const data = encoder.encode(password);
        
        // 生成随机 IV（12字节用于 GCM）
        const iv = crypto.getRandomValues(new Uint8Array(12));
        
        // 使用 AES-GCM 加密
        const encrypted = await crypto.subtle.encrypt(
          {
            name: 'AES-GCM',
            iv: iv
          },
          key,
          data
        );
        
        // 组合 IV + 密文
        const combined = new Uint8Array(iv.length + encrypted.byteLength);
        combined.set(iv);
        combined.set(new Uint8Array(encrypted), iv.length);
        
        // Base64 编码
        const result = btoa(String.fromCharCode(...combined));
        
        return result;
      } else {
        // 降级使用 node-forge (真正的 AES-GCM)
        return encryptPasswordWithForge(password, salt, timestamp);
      }
    } catch (error) {
      console.error('❌ AES-GCM加密失败:', error);
      throw new Error('密码加密失败');
    }
  }
  
  /**
   * 安全登录管理器
   */
  class SecureLoginManager {
    private saltCache: any = null;
    private saltPromise: Promise<any> | null = null;
  
    /**
     * 智能获取盐值（支持缓存和重入）
     */
    async getLoginSalt(username: string): Promise<any> {
      // 如果有正在进行的请求，等待它完成
      if (this.saltPromise) {
        return await this.saltPromise;
      }
  
      // 检查缓存的盐值是否还有效
      if (this.saltCache && this.saltCache.username === username) {
        const now = Date.now() / 1000;
        const expiresAt = this.saltCache.timestamp + this.saltCache.expires_in;
        
        if (now < expiresAt - 30) { // 提前30秒过期
          return this.saltCache;
        }
      }
  
      // 创建新的盐值请求
      this.saltPromise = this._fetchSalt(username);
      
      try {
        const saltData = await this.saltPromise;
        this.saltCache = { ...saltData, username };
        return saltData;
      } finally {
        this.saltPromise = null;
      }
    }
  
    private async _fetchSalt(username: string): Promise<any> {
      const response = await fetch(gatewayURL('/api/admin/auth/GetLoginSalt'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
        app_info: getAppInfo(),
        username: username
      })
      });
      
      const data = await response.json();
      
      // 添加安全检查
      if (!data) {
        throw new Error('获取盐值失败：响应数据为空');
      }
      
      if (!data.ret_info) {
        throw new Error('获取盐值失败：响应格式错误，缺少ret_info字段');
      }
      
      // 使用新的ret_info协议格式
      if (data.ret_info.code !== 0) {
        throw new Error(data.ret_info.msg || '获取盐值失败');
      }
      
      return data;
    }
  
    /**
     * 安全登录
     */
    async login(username: string, password: string): Promise<any> {
      try {
        // 1. 获取动态盐值
        const saltData = await this.getLoginSalt(username);
  
        // 2. 加密密码
        const encryptedPassword = await encryptPassword(password, saltData.salt, saltData.timestamp);
  
        // 3. 构建登录请求（严格按照 LoginReq 协议）
        const loginRequest = {
          app_info: getAppInfo(),
          username: username,
          password_hash: encryptedPassword,
          salt: saltData.salt,
          timestamp: saltData.timestamp,
          device_id: generateDeviceId(),
          user_agent: navigator.userAgent,
          client_ip: getClientIP()
          // 注意：移除 verify_code 字段，因为后端协议中没有定义
        };
  
        // 4. 发送登录请求
        const response = await fetch(gatewayURL('/api/admin/auth/Login'), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(loginRequest)
        });
  
        const result = await response.json();
        
        // 添加安全检查
        if (!result) {
          throw new Error('登录失败：响应数据为空');
        }
        
        if (!result.ret_info) {
          throw new Error('登录失败：响应格式错误，缺少ret_info字段');
        }
        
        // 使用新的ret_info协议格式
        if (result.ret_info.code !== 0) {
          throw new Error(result.ret_info.msg || '登录失败');
        }
        
        return result;
        
      } catch (error: unknown) {
        console.error('❌ 安全登录失败:', error);
        throw error;
      }
    }
  }
  
  // 导出单例
  export const secureLoginManager = new SecureLoginManager();
