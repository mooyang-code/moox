import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'MooX',
  description: '面向个人的一站式量化平台',
  lang: 'zh-CN',
  base: '/moox/',
  lastUpdated: true,
  cleanUrls: true,
  ignoreDeadLinks: true,

  head: [
    ['meta', { name: 'theme-color', content: '#3c8772' }],
  ],

  themeConfig: {
    outline: {
      label: '本页目录',
      level: [2, 3],
    },

    docFooter: {
      prev: '上一页',
      next: '下一页',
    },

    lastUpdatedText: '最后更新',

    returnToTopLabel: '回到顶部',
    sidebarMenuLabel: '菜单',

    search: {
      provider: 'local',
      options: {
        translations: {
          button: {
            buttonText: '搜索',
            buttonAriaLabel: '搜索',
          },
          modal: {
            noResultsText: '无法找到相关结果',
            resetButtonTitle: '清除查询条件',
            footer: {
              selectText: '选择',
              navigateText: '切换',
            },
          },
        },
      },
    },

    sidebar: [
      {
        text: '总体',
        items: [
          { text: '文档索引', link: '/README' },
          { text: '当前实现与文档边界', link: '/实现状态' },
          { text: '总体设计', link: '/总体设计' },
          { text: '构建手册', link: '/构建手册' },
          { text: '部署与运维', link: '/部署与运维' },
          { text: '元数据命名规范', link: '/元数据命名规范' },
        ],
      },
      {
        text: '控制面',
        items: [
          { text: '管理后台', link: '/模块/管理后台' },
          { text: '主机网关', link: '/模块/主机网关' },
          { text: '外部接入', link: '/模块/外部接入' },
          { text: '出口代理', link: '/模块/出口代理' },
          { text: '事件总线', link: '/模块/事件总线' },
          { text: '控制台代理', link: '/模块/控制台代理' },
        ],
      },
      {
        text: '数据',
        items: [
          { text: '存储', link: '/模块/存储' },
          { text: '采集', link: '/模块/采集' },
          { text: '云节点', link: '/模块/云节点' },
          { text: '归档', link: '/模块/归档' },
        ],
      },
      {
        text: '量化',
        items: [
          { text: '因子', link: '/模块/因子' },
          { text: '策略', link: '/模块/策略' },
          { text: '交易', link: '/模块/交易' },
        ],
      },
      {
        text: '运维与工具',
        items: [
          { text: '监控', link: '/模块/监控' },
          { text: '主机代理', link: '/模块/主机代理' },
          { text: '命令行工具', link: '/模块/命令行工具' },
          { text: '前端', link: '/模块/前端' },
          { text: '共享包', link: '/模块/共享包' },
        ],
      },
      {
        text: '审查与历史记录',
        collapsed: true,
        items: [
          { text: '代码审查与优化建议', link: '/审查/2026-10-10代码审查' },
          { text: '计划索引', link: '/计划/README' },
          { text: '策略重建设计', link: '/计划/策略模块评审与设计' },
          { text: '策略重建执行计划', link: '/计划/策略模块执行计划' },
          { text: '数据保留与配置发布', link: '/计划/数据保留与配置发布' },
          { text: '网关与服务部署重构设计', link: '/计划/网关与服务部署重构设计' },
          { text: '网关与服务部署重构执行计划', link: '/计划/网关与服务部署重构执行计划' },
          { text: '秘密管理与初始化录入设计', link: '/计划/秘密管理与初始化录入设计' },
          { text: 'Console Proxy 实施记录', link: '/2026-10-09-console-proxy-embedded-caddy-execution-plan' },
          { text: 'Timer 写入压测记录', link: '/计划/Timer模式Storage写入压测报告' },
        ],
      },
    ],

    socialLinks: [
      { icon: 'github', link: 'https://github.com/mooyang-code/moox' },
    ],

    footer: {
      message: '基于 CC BY-NC-SA 4.0 发布',
      copyright: 'Copyright © 2026 mooyang-code',
    },
  },
})
