import { defineConfig, loadEnv } from "vite";
import path from "path";
import { resolve } from "path";
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
            'utils-crypto': ['node-forge'],
            
            // 终端相关 - 按需加载
            'terminal-core': ['@xterm/xterm'],
            'terminal-addons': ['@xterm/addon-attach', '@xterm/addon-fit'],
            
          }
        }
      }
    },
    server: {
      // host: "0.0.0.0",
      port: 9527,
      open: false
    }
  };
});
