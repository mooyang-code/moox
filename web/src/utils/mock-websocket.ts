/**
 * 模拟WebSocket服务，用于演示SSH终端功能
 * 实际生产环境中应该连接到真实的WebSocket服务
 */

export class MockWebSocket {
  private url: string;
  private readyState: number = WebSocket.CONNECTING;
  private listeners: { [key: string]: Function[] } = {};
  private sessionId: string;
  private currentPath: string = '/root';
  private commandHistory: string[] = [];

  // WebSocket状态常量
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  constructor(url: string) {
    this.url = url;
    this.sessionId = this.extractSessionId(url);
    
    // 模拟连接延迟
    setTimeout(() => {
      this.readyState = WebSocket.OPEN;
      this.dispatchEvent('open', {});
      this.sendWelcomeMessage();
    }, 500);
  }

  private extractSessionId(url: string): string {
    const match = url.match(/session_id=([^&]+)/);
    return match ? match[1] : 'mock_session';
  }

  private sendWelcomeMessage() {
    setTimeout(() => {
      this.dispatchEvent('message', {
        data: '\r\n欢迎使用容器SSH终端\r\n'
      });
      this.sendPrompt();
    }, 100);
  }

  private sendPrompt() {
    setTimeout(() => {
      this.dispatchEvent('message', {
        data: `\r\nroot@container:${this.currentPath}$ `
      });
    }, 50);
  }

  private processCommand(command: string) {
    const cmd = command.trim();
    this.commandHistory.push(cmd);

    // 模拟各种命令的输出
    switch (cmd.toLowerCase()) {
      case 'ls':
      case 'ls -la':
        this.dispatchEvent('message', {
          data: '\r\ntotal 64\r\ndrwxr-xr-x  1 root root  4096 Jan 15 10:30 .\r\ndrwxr-xr-x  1 root root  4096 Jan 15 10:30 ..\r\n-rw-r--r--  1 root root   220 Jan 15 10:30 .bashrc\r\n-rw-r--r--  1 root root   807 Jan 15 10:30 .profile\r\ndrwxr-xr-x  2 root root  4096 Jan 15 10:30 app\r\ndrwxr-xr-x  2 root root  4096 Jan 15 10:30 bin\r\ndrwxr-xr-x  2 root root  4096 Jan 15 10:30 etc\r\ndrwxr-xr-x  2 root root  4096 Jan 15 10:30 home\r\ndrwxr-xr-x  2 root root  4096 Jan 15 10:30 tmp\r\ndrwxr-xr-x  2 root root  4096 Jan 15 10:30 var\r\n'
        });
        break;

      case 'pwd':
        this.dispatchEvent('message', {
          data: `\r\n${this.currentPath}\r\n`
        });
        break;

      case 'whoami':
        this.dispatchEvent('message', {
          data: '\r\nroot\r\n'
        });
        break;

      case 'date':
        this.dispatchEvent('message', {
          data: `\r\n${new Date().toString()}\r\n`
        });
        break;

      case 'ps aux':
        this.dispatchEvent('message', {
          data: '\r\nUSER       PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND\r\nroot         1  0.0  0.1  18508  3396 ?        Ss   10:30   0:00 /bin/bash\r\nroot        15  0.0  0.1  34424  2896 ?        R    10:35   0:00 ps aux\r\n'
        });
        break;

      case 'df -h':
        this.dispatchEvent('message', {
          data: '\r\nFilesystem      Size  Used Avail Use% Mounted on\r\noverlay          59G   15G   42G  27% /\r\ntmpfs            64M     0   64M   0% /dev\r\ntmpfs           2.0G     0  2.0G   0% /sys/fs/cgroup\r\n/dev/sda1        59G   15G   42G  27% /etc/hosts\r\nshm              64M     0   64M   0% /dev/shm\r\n'
        });
        break;

      case 'free -h':
        this.dispatchEvent('message', {
          data: '\r\n              total        used        free      shared  buff/cache   available\r\nMem:           3.9G        1.2G        1.8G         12M        896M        2.5G\r\nSwap:          2.0G          0B        2.0G\r\n'
        });
        break;

      case 'top':
        this.dispatchEvent('message', {
          data: '\r\ntop - 10:35:42 up 5 min,  0 users,  load average: 0.08, 0.03, 0.01\r\nTasks:   2 total,   1 running,   1 sleeping,   0 stopped,   0 zombie\r\n%Cpu(s):  0.3 us,  0.7 sy,  0.0 ni, 99.0 id,  0.0 wa,  0.0 hi,  0.0 si,  0.0 st\r\nMiB Mem :   3984.4 total,   1876.2 free,   1212.1 used,    896.1 buff/cache\r\nMiB Swap:   2048.0 total,   2048.0 free,      0.0 used.   2544.2 avail Mem\r\n\r\n  PID USER      PR  NI    VIRT    RES    SHR S  %CPU  %MEM     TIME+ COMMAND\r\n    1 root      20   0   18508   3396   2944 S   0.0   0.1   0:00.02 bash\r\n   15 root      20   0   38620   3264   2816 R   0.0   0.1   0:00.00 top\r\n'
        });
        break;

      case 'clear':
        this.dispatchEvent('message', {
          data: '\x1b[2J\x1b[H'
        });
        break;

      case 'history':
        let historyOutput = '\r\n';
        this.commandHistory.forEach((cmd, index) => {
          historyOutput += `${index + 1}  ${cmd}\r\n`;
        });
        this.dispatchEvent('message', {
          data: historyOutput
        });
        break;

      case 'help':
        this.dispatchEvent('message', {
          data: '\r\n可用命令:\r\nls, pwd, whoami, date, ps aux, df -h, free -h, top, clear, history, help\r\ncd <directory>, cat <file>, echo <text>\r\n这是一个演示终端，支持基本的Linux命令模拟\r\n'
        });
        break;

      default:
        if (cmd.startsWith('cd ')) {
          const newPath = cmd.substring(3).trim();
          if (newPath === '..') {
            const pathParts = this.currentPath.split('/').filter(p => p);
            pathParts.pop();
            this.currentPath = '/' + pathParts.join('/');
            if (this.currentPath === '/') this.currentPath = '/root';
          } else if (newPath.startsWith('/')) {
            this.currentPath = newPath;
          } else {
            this.currentPath = this.currentPath === '/' ? `/${newPath}` : `${this.currentPath}/${newPath}`;
          }
        } else if (cmd.startsWith('echo ')) {
          const text = cmd.substring(5);
          this.dispatchEvent('message', {
            data: `\r\n${text}\r\n`
          });
        } else if (cmd.startsWith('cat ')) {
          const filename = cmd.substring(4).trim();
          this.dispatchEvent('message', {
            data: `\r\n这是文件 ${filename} 的内容\r\n这是一个演示文件\r\n包含一些示例文本\r\n`
          });
        } else if (cmd === '') {
          // 空命令，不输出任何内容
        } else {
          this.dispatchEvent('message', {
            data: `\r\nbash: ${cmd}: command not found\r\n`
          });
        }
        break;
    }

    // 发送新的提示符
    this.sendPrompt();
  }

  // 模拟WebSocket的addEventListener方法
  addEventListener(type: string, listener: Function) {
    if (!this.listeners[type]) {
      this.listeners[type] = [];
    }
    this.listeners[type].push(listener);
  }

  // 模拟WebSocket的removeEventListener方法
  removeEventListener(type: string, listener: Function) {
    if (this.listeners[type]) {
      const index = this.listeners[type].indexOf(listener);
      if (index > -1) {
        this.listeners[type].splice(index, 1);
      }
    }
  }

  // 分发事件
  private dispatchEvent(type: string, event: any) {
    if (this.listeners[type]) {
      this.listeners[type].forEach(listener => {
        listener(event);
      });
    }

    // 同时支持onopen, onmessage等属性方式的事件处理
    const handlerName = `on${type}` as keyof this;
    if (typeof this[handlerName] === 'function') {
      (this[handlerName] as Function)(event);
    }
  }

  // 模拟WebSocket的send方法
  send(data: string) {
    if (this.readyState !== WebSocket.OPEN) {
      throw new Error('WebSocket is not open');
    }

    // 处理输入的数据
    if (data.includes('\r') || data.includes('\n')) {
      // 如果包含回车或换行，处理为命令
      const command = data.replace(/[\r\n]/g, '');
      this.processCommand(command);
    } else {
      // 否则回显输入的字符
      this.dispatchEvent('message', { data });
    }
  }

  // 模拟WebSocket的close方法
  close() {
    this.readyState = WebSocket.CLOSING;
    setTimeout(() => {
      this.readyState = WebSocket.CLOSED;
      this.dispatchEvent('close', {});
    }, 100);
  }

  // WebSocket属性
  get url() { return this.url; }
  get readyState() { return this.readyState; }

  // 事件处理器属性
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
}
