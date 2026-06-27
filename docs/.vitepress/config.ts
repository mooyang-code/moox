import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'MooX',
  description: '一站式量化金融数据平台',
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
        text: '开始',
        items: [
          { text: '前言', link: '/前言' },
        ],
      },
      {
        text: '第一部分：整体架构',
        collapsed: false,
        items: [
          { text: '架构总览', link: '/架构总览' },
          { text: '大仓架构', link: '/大仓架构' },
          { text: '协议设计', link: '/协议设计' },
        ],
      },
      {
        text: '第二部分：存储引擎',
        collapsed: false,
        items: [
          { text: '量化金融数据概念', link: '/量化金融数据概念' },
          { text: '存储概念与设计意图', link: '/存储概念与设计意图' },
          { text: '存储目标架构与元数据', link: '/存储目标架构与元数据' },
          { text: '存储引擎架构', link: '/存储引擎架构' },
          { text: '性能基准报告', link: '/性能基准报告/存储基准测试-20260620' },
        ],
      },
      {
        text: '第三部分：管理服务',
        collapsed: false,
        items: [
          { text: '认证鉴权', link: '/认证鉴权' },
          { text: '数据库管理', link: '/数据库管理' },
          { text: '异步任务', link: '/异步任务' },
          { text: '云节点管理', link: '/云节点管理' },
          { text: '代码包管理', link: '/代码包管理' },
          { text: '采集任务管理', link: '/采集任务管理' },
          { text: '监控配置', link: '/监控配置' },
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
