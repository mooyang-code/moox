import { defineConfig, loadEnv } from "vite";
import path from "path";
import { resolve } from "path";
import { include } from "./build/optimize";
import postcssPresetEnv from "postcss-preset-env";
import { createVitePlugins } from "./build/vite-plugin";

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  // 根路径
  const root = process.cwd();
  // 获取跟路径对应的文件
  const env: any = loadEnv(mode, root);
  return {
    base: mode === 'production' ? './' : '/',
    plugins: createVitePlugins(env),
    resolve: {
      alias: {
        "@assets": path.join(__dirname, "src/assets"),
        "@": resolve(__dirname, "./src")
      }
    },
    css: {
      postcss: {
        plugins: [postcssPresetEnv()]
      },
      preprocessorOptions: {
        scss: {
          // additionalData的内容会在每个scss文件的开头自动注入
          additionalData: `@use "@/style/var/index.scss" as *; `
        }
      }
    },
    // 依赖预加载 https://cn.vitejs.dev/config/dep-optimization-options.html#dep-optimization-options
    optimizeDeps: {
      include,
      // 强制预构建链接的包
      force: true
    },
    build: {
      outDir: "dist", // 指定打包路径，默认为项目根目录下的dist目录
      minify: "esbuild", // Use esbuild to avoid the rollup error
      assetsInlineLimit: 2 * 1024, // 进一步降低内联阈值到2kb
      chunkSizeWarningLimit: 1000, // 提高警告阈值到1MB
      // 开启源码映射用于生产环境调试（可选）
      sourcemap: false,
      // 启用CSS代码拆分
      cssCodeSplit: true,
      // 启用压缩
      reportCompressedSize: false, // 禁用压缩大小报告以加快构建
      target: ['es2020', 'edge88', 'firefox78', 'chrome87', 'safari14'], // 更现代的浏览器目标
      // 静态资源打包到dist下的不同目录,将文件类型css、js、jpg等文件分开存储
      rollupOptions: {
        output: {
          chunkFileNames: "static/js/[name]-[hash].js",
          entryFileNames: "static/js/[name]-[hash].js",
          assetFileNames: "static/[ext]/[name]-[hash].[ext]",
          // 更细粒度的代码分割
          manualChunks: {
            // Vue核心 - 优先级最高，应该最先加载
            'vue-core': ['vue', 'vue-router'],
            'vue-store': ['pinia', 'pinia-plugin-persistedstate'],
            
            // UI组件库 - 按需分割
            'arco-base': ['@arco-design/web-vue'],
            'arco-utils': ['@arco-design/color'],
            
            // 工具库 - 最基础的
            'utils-core': ['axios'],
            'utils-crypto': ['crypto-js', 'node-forge'],
            'utils-misc': ['js-yaml', 'qrcode', 'jsbarcode', 'print-js'],
            
            // 代码编辑器相关 - 按需加载
            'editor-core': ['codemirror'],
            'editor-vue': ['vue-codemirror', 'vue-codemirror6'],
            'editor-langs': ['@codemirror/lang-javascript', '@codemirror/lang-json', '@codemirror/lang-vue', '@codemirror/lang-yaml'],
            'editor-themes': ['@codemirror/theme-one-dark'],
            
            // 终端相关 - 按需加载
            'terminal-core': ['@xterm/xterm'],
            'terminal-addons': ['@xterm/addon-attach', '@xterm/addon-fit'],
            
            // 交互相关
            'interaction': ['vuedraggable', 'sortablejs', 'driver.js'],
            
            // 指纹和识别
            'fingerprint': ['fingerprintjs2', '@fingerprintjs/fingerprintjs'],
            
            // 其他工具
            'pinyin': ['pinyin-pro']
          }
        }
      }
    },
    server: {
      // host: "0.0.0.0",
      open: false,
      // 为开发服务器配置自定义代理规则-用于开发时的代理
      proxy: {
        "/gateway": {
          target: "http://localhost:20103",
          changeOrigin: true,
          secure: false
        },
        "/trpc.moox.server": {
          target: "http://localhost:20102", 
          changeOrigin: true,
          secure: false
        }
      }
    }
  };
});
