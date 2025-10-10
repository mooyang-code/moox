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
      assetsInlineLimit: 50 * 1024, // 打包内联阈值100kb
      chunkSizeWarningLimit: 50000, // 规定触发警告的 chunk 大小, 这里设置阈值为50kb, 消除打包大小超过500kb警告
      // 静态资源打包到dist下的不同目录,将文件类型css、js、jpg等文件分开存储
      rollupOptions: {
        output: {
          chunkFileNames: "static/js/[name]-[hash].js",
          entryFileNames: "static/js/[name]-[hash].js",
          assetFileNames: "static/[ext]/[name]-[hash].[ext]",
          // Prevent circular dependencies
          manualChunks(id) {
            if (id.includes('node_modules')) {
              return 'vendor';
            }
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
