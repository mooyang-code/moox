/**
 * 安全加密工具模块
 * 使用 Web Crypto API 实现 AES-GCM 加密，与后端兼容
 */

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
   * 从盐值和时间戳派生加密密钥
   */
  async function deriveEncryptionKey(salt: string, timestamp: number): Promise<CryptoKey> {
    const keyMaterial = salt + timestamp.toString();
    const encoder = new TextEncoder();
    const data = encoder.encode(keyMaterial);
    
    // 使用 SHA-256 生成密钥材料
    const hashBuffer = await crypto.subtle.digest('SHA-256', data);
    
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
   * 使用 AES-GCM 加密密码（与后端兼容）
   */
  export async function encryptPassword(password: string, salt: string, timestamp: number): Promise<string> {
    try {
      console.log('🔐 开始AES-GCM加密...', { salt, timestamp });
      
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
      console.log('✅ AES-GCM加密成功', { 
        passwordLength: password.length,
        encryptedLength: result.length,
        encrypted: result 
      });
      
      return result;
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
          console.log('📋 使用缓存的盐值');
          return this.saltCache;
        }
      }
  
      // 创建新的盐值请求
      this.saltPromise = this._fetchSalt(username);
      
      try {
        const saltData = await this.saltPromise;
        this.saltCache = { ...saltData, username };
        console.log('🔄 获取新盐值成功', this.saltCache);
        return saltData;
      } finally {
        this.saltPromise = null;
      }
    }
  
    private async _fetchSalt(username: string): Promise<any> {
      console.log('🌐 请求新的登录盐值...', { username });
      
      const response = await fetch(`/gateway/auth/GetLoginSalt`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
        app_info: {
          app_id: "moox_frontend",
          app_key: "2521e0d21b6be0347b72bca93904a0dd"
        },
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
      console.log('🚀 开始安全登录流程...', { username });
      
      try {
        // 1. 获取动态盐值
        const saltData = await this.getLoginSalt(username);
        console.log('📝 获取盐值:', {
          salt: saltData.salt,
          timestamp: saltData.timestamp,
          expiresIn: saltData.expiresIn
        });
  
        // 2. 加密密码
        const encryptedPassword = await encryptPassword(password, saltData.salt, saltData.timestamp);
        console.log('🔒 密码加密完成');
  
        // 3. 构建登录请求（严格按照 LoginReq 协议）
        const loginRequest = {
          app_info: {
            app_id: "moox_frontend",
            app_key: "2521e0d21b6be0347b72bca93904a0dd"
          },
          username: username,
          password_hash: encryptedPassword,
          salt: saltData.salt,
          timestamp: saltData.timestamp,
          device_id: generateDeviceId(),
          user_agent: navigator.userAgent,
          client_ip: getClientIP()
          // 注意：移除 verify_code 字段，因为后端协议中没有定义
        };
  
        console.log('📤 发送登录请求:', {
          username: loginRequest.username,
          salt: loginRequest.salt,
          timestamp: loginRequest.timestamp,
          device_id: loginRequest.device_id
        });
  
        // 4. 发送登录请求
        const response = await fetch(`/gateway/auth/Login`, {
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
        
        console.log('✅ 安全登录成功');
        return result;
        
      } catch (error: any) {
        console.error('❌ 安全登录失败:', error);
        throw error;
      }
    }
  }
  
  // 导出单例
  export const secureLoginManager = new SecureLoginManager();