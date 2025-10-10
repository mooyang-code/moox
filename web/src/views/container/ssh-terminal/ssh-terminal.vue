<template>
  <div class="ssh-terminal-page">
    <div class="page-header">
      <h2>SSH终端</h2>
      <p>连接到容器并执行命令</p>
    </div>
    
    <div class="page-content">
      <a-row :gutter="20">
        <!-- 容器选择 -->
        <a-col :span="24">
          <a-card title="连接设置" :bordered="false" class="connection-card">
            <a-row :gutter="16">
              <a-col :span="8">
                <a-form-item label="选择容器">
                  <a-select 
                    v-model="selectedContainer" 
                    placeholder="请选择要连接的容器"
                    @change="onContainerChange"
                  >
                    <a-option 
                      v-for="container in containers" 
                      :key="container.id" 
                      :value="container.id"
                    >
                      {{ container.name }}
                    </a-option>
                  </a-select>
                </a-form-item>
              </a-col>
              <a-col :span="8">
                <a-form-item label="连接状态">
                  <a-tag :color="connectionStatus === 'connected' ? 'green' : 'red'">
                    {{ connectionStatus === 'connected' ? '已连接' : '未连接' }}
                  </a-tag>
                </a-form-item>
              </a-col>
              <a-col :span="8">
                <a-form-item label="操作">
                  <a-space>
                    <a-button 
                      type="primary" 
                      :loading="connecting"
                      @click="connect"
                      :disabled="!selectedContainer"
                    >
                      {{ connectionStatus === 'connected' ? '重新连接' : '连接' }}
                    </a-button>
                    <a-button 
                      @click="disconnect"
                      :disabled="connectionStatus !== 'connected'"
                    >
                      断开连接
                    </a-button>
                  </a-space>
                </a-form-item>
              </a-col>
            </a-row>
          </a-card>
        </a-col>
        
        <!-- 终端区域 -->
        <a-col :span="24">
          <a-card title="终端" :bordered="false" class="terminal-card">
            <template #extra>
              <a-space>
                <a-button size="small" @click="clearTerminal">
                  <template #icon>
                    <icon-delete />
                  </template>
                  清空
                </a-button>
                <a-button size="small" @click="downloadLog">
                  <template #icon>
                    <icon-download />
                  </template>
                  下载日志
                </a-button>
              </a-space>
            </template>
            
            <div class="terminal-container">
              <div 
                ref="terminalRef" 
                class="terminal"
                :class="{ 'terminal-disabled': connectionStatus !== 'connected' }"
              >
                <div v-for="(line, index) in terminalLines" :key="index" class="terminal-line">
                  <span class="terminal-prompt" v-if="line.type === 'prompt'">{{ line.prompt }}</span>
                  <span class="terminal-command" v-if="line.type === 'command'">{{ line.content }}</span>
                  <span class="terminal-output" v-if="line.type === 'output'">{{ line.content }}</span>
                  <span class="terminal-error" v-if="line.type === 'error'">{{ line.content }}</span>
                </div>
                <div v-if="connectionStatus === 'connected'" class="terminal-input-line">
                  <span class="terminal-prompt">{{ currentPrompt }}</span>
                  <input 
                    ref="commandInput"
                    v-model="currentCommand"
                    class="terminal-input"
                    @keydown.enter="executeCommand"
                    @keydown.up="previousCommand"
                    @keydown.down="nextCommand"
                    placeholder="输入命令..."
                  />
                </div>
              </div>
            </div>
          </a-card>
        </a-col>
      </a-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, nextTick } from 'vue';
import { useRoute } from 'vue-router';
import { Message } from '@arco-design/web-vue';

const route = useRoute();
const terminalRef = ref();
const commandInput = ref();

// 状态管理
const selectedContainer = ref('');
const connectionStatus = ref<'connected' | 'disconnected'>('disconnected');
const connecting = ref(false);
const currentCommand = ref('');
const currentPrompt = ref('root@container:~$ ');

// 容器列表
const containers = ref([
  { id: 'container-001', name: 'moox-backend-1' },
  { id: 'container-002', name: 'moox-database-1' },
  { id: 'container-003', name: 'moox-redis-1' }
]);

// 终端行接口定义
interface TerminalLine {
  type: string;
  content?: string;
  prompt?: string;
}

// 终端输出
const terminalLines = ref<TerminalLine[]>([
  { type: 'output', content: '欢迎使用 MooX 容器终端' },
  { type: 'output', content: '请选择容器并连接以开始使用' }
]);

// 命令历史
const commandHistory = ref<string[]>([]);
const historyIndex = ref(-1);

// 容器变更
const onContainerChange = () => {
  if (connectionStatus.value === 'connected') {
    disconnect();
  }
};

// 连接容器
const connect = async () => {
  if (!selectedContainer.value) {
    Message.warning('请先选择容器');
    return;
  }
  
  connecting.value = true;
  try {
    // 模拟连接过程
    await new Promise(resolve => setTimeout(resolve, 1500));
    
    connectionStatus.value = 'connected';
    const containerName = containers.value.find(c => c.id === selectedContainer.value)?.name;
    
    terminalLines.value.push(
      { type: 'output', content: `正在连接到容器 ${containerName}...` },
      { type: 'output', content: '连接成功!' },
      { type: 'output', content: '您现在可以执行命令了。' }
    );
    
    Message.success('连接成功');
    
    // 聚焦到输入框
    await nextTick();
    commandInput.value?.focus();
    
  } catch (error) {
    Message.error('连接失败');
  } finally {
    connecting.value = false;
  }
};

// 断开连接
const disconnect = () => {
  connectionStatus.value = 'disconnected';
  terminalLines.value.push(
    { type: 'output', content: '连接已断开' }
  );
  currentCommand.value = '';
  Message.info('已断开连接');
};

// 执行命令
const executeCommand = () => {
  if (!currentCommand.value.trim()) return;
  
  const command = currentCommand.value.trim();
  
  // 添加到历史记录
  commandHistory.value.push(command);
  historyIndex.value = commandHistory.value.length;
  
  // 显示命令
  terminalLines.value.push(
    { type: 'prompt', prompt: currentPrompt.value },
    { type: 'command', content: command }
  );
  
  // 模拟命令执行
  executeCommandSimulation(command);
  
  currentCommand.value = '';
};

// 模拟命令执行
const executeCommandSimulation = (command: string) => {
  // 简单的命令模拟
  switch (command.toLowerCase()) {
    case 'ls':
      terminalLines.value.push(
        { type: 'output', content: 'app  bin  boot  dev  etc  home  lib  media  mnt  opt  proc  root  run  sbin  srv  sys  tmp  usr  var' }
      );
      break;
    case 'pwd':
      terminalLines.value.push(
        { type: 'output', content: '/root' }
      );
      break;
    case 'whoami':
      terminalLines.value.push(
        { type: 'output', content: 'root' }
      );
      break;
    case 'date':
      terminalLines.value.push(
        { type: 'output', content: new Date().toString() }
      );
      break;
    case 'clear':
      terminalLines.value = [];
      return;
    case 'help':
      terminalLines.value.push(
        { type: 'output', content: '可用命令: ls, pwd, whoami, date, clear, help' },
        { type: 'output', content: '这是一个演示终端，实际使用时会连接到真实容器' }
      );
      break;
    default:
      if (command.startsWith('echo ')) {
        terminalLines.value.push(
          { type: 'output', content: command.substring(5) }
        );
      } else {
        terminalLines.value.push(
          { type: 'error', content: `bash: ${command}: command not found` }
        );
      }
  }
  
  // 滚动到底部
  nextTick(() => {
    if (terminalRef.value) {
      terminalRef.value.scrollTop = terminalRef.value.scrollHeight;
    }
  });
};

// 上一个命令
const previousCommand = () => {
  if (historyIndex.value > 0) {
    historyIndex.value--;
    currentCommand.value = commandHistory.value[historyIndex.value];
  }
};

// 下一个命令
const nextCommand = () => {
  if (historyIndex.value < commandHistory.value.length - 1) {
    historyIndex.value++;
    currentCommand.value = commandHistory.value[historyIndex.value];
  } else {
    historyIndex.value = commandHistory.value.length;
    currentCommand.value = '';
  }
};

// 清空终端
const clearTerminal = () => {
  terminalLines.value = [];
};

// 下载日志
const downloadLog = () => {
  const logContent = terminalLines.value
    .map(line => {
      if (line.type === 'prompt') return line.prompt;
      return line.content;
    })
    .join('\n');
    
  const blob = new Blob([logContent], { type: 'text/plain' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `terminal-log-${new Date().getTime()}.txt`;
  a.click();
  URL.revokeObjectURL(url);
  
  Message.success('日志已下载');
};

onMounted(() => {
  // 如果URL中有容器ID参数，自动选择
  const containerId = route.query.containerId as string;
  if (containerId) {
    selectedContainer.value = containerId;
  }
});
</script>

<style lang="scss" scoped>
.ssh-terminal-page {
  padding: 20px;
  height: calc(100vh - 120px);
  
  .page-header {
    margin-bottom: 20px;
    
    h2 {
      margin: 0 0 8px 0;
      font-size: 24px;
      font-weight: 600;
    }
    
    p {
      margin: 0;
      color: var(--color-text-2);
    }
  }
  
  .page-content {
    height: calc(100% - 80px);
    
    .connection-card {
      margin-bottom: 20px;
    }
    
    .terminal-card {
      height: calc(100% - 140px);
      
      :deep(.arco-card-body) {
        height: calc(100% - 60px);
        padding: 0;
      }
    }
  }
  
  .terminal-container {
    height: 100%;
    background: #1e1e1e;
    border-radius: 4px;
    overflow: hidden;
  }
  
  .terminal {
    height: 100%;
    padding: 16px;
    font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
    font-size: 14px;
    line-height: 1.4;
    color: #d4d4d4;
    background: #1e1e1e;
    overflow-y: auto;
    
    &.terminal-disabled {
      opacity: 0.6;
    }
  }
  
  .terminal-line {
    margin-bottom: 2px;
    word-wrap: break-word;
  }
  
  .terminal-prompt {
    color: #4ec9b0;
    font-weight: bold;
  }
  
  .terminal-command {
    color: #d4d4d4;
    margin-left: 8px;
  }
  
  .terminal-output {
    color: #d4d4d4;
  }
  
  .terminal-error {
    color: #f44747;
  }
  
  .terminal-input-line {
    display: flex;
    align-items: center;
    margin-top: 8px;
  }
  
  .terminal-input {
    flex: 1;
    background: transparent;
    border: none;
    outline: none;
    color: #d4d4d4;
    font-family: inherit;
    font-size: inherit;
    margin-left: 8px;
    
    &::placeholder {
      color: #6a6a6a;
    }
  }
}
</style>
