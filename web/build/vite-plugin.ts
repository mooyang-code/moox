import vue from "@vitejs/plugin-vue";
import { resolve } from "path";
import { PluginOption } from "vite";
import { vitePluginForArco } from "@arco-plugins/vite-vue";
import { createHtmlPlugin } from "vite-plugin-html";
import { createSvgIconsPlugin } from "vite-plugin-svg-icons";
import AutoImport from "unplugin-auto-import/vite";
import { ArcoResolver } from "unplugin-vue-components/resolvers";
import Components from "unplugin-vue-components/vite";
import eslintPlugin from "vite-plugin-eslint";
/**
 * 创建 vite 插件
 * @param viteEnv
 */
export const createVitePlugins = (viteEnv: ViteEnv): (PluginOption | PluginOption[])[] => {
  const env = viteEnv;
  const plugins = [
    vue(),
    // esLint 报错信息显示在浏览器界面上
    // Temporarily disable eslint plugin during build to avoid rollup error
    process.env.NODE_ENV === 'production' ? null : eslintPlugin(),
    vitePluginForArco({
      style: "css"
    }),
    // 提供ejs模板能力，用于index.html的标题显示
    createHtmlPlugin({
      inject: {
        data: {
          title: env.VITE_GLOB_APP_TITLE  + " 一站式量化平台",
          mode: process.env.NODE_ENV || "development"
        }
      }
    }),
    createSvgIconsPlugin({
      // 配置src下存放svg的路径，这里表示在src/assets/svgs文件夹下
      iconDirs: [resolve(process.cwd(), "src/assets/svgs")],
      symbolId: "icon-[dir]-[name]"
    }),
    AutoImport({
      // 自动导入 Vue 相关函数，如：ref, reactive, toRef 等
      imports: ["vue", "vue-router"],
      // arco组件的按需加载
      resolvers: [ArcoResolver()],
      // 解决eslint报错问题
      eslintrc: {
        // 这里先设置成true然后npm run dev 运行之后会生成 .eslintrc-auto-import.json 文件之后，在改为false
        enabled: false,
        filepath: "./.eslintrc-auto-import.json", // 生成的文件路径
        globalsPropValue: true
      },
      // 配置文件生成位置
      dts: "src/auto-import.d.ts"
    }),
    Components({
      resolvers: [
        // arco组件的按需加载
        ArcoResolver({
          sideEffect: true
        })
      ],
      // 自动加载组件的目录配置,默认的为 'src/components'
      dirs: ["src/components"],
      // 组件的有效文件扩展名
      extensions: ["vue"],
      // 配置文件生成位置
      dts: "src/components.d.ts"
    }),
  ];
  
  return plugins.filter(Boolean) as (PluginOption | PluginOption[])[];
};
