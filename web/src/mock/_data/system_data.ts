// 角色数据
export const roleData = [
  {
    id: 1,
    name: "超级管理员",
    code: "admin",
    sort: 1,
    status: 1,
    admin: true,
    description: "默认角色，超级管理员，上帝角色",
    createBy: null,
    createTime: "2025-03-27 11:21:01",
    updateBy: null,
    updateTime: null
  },
  {
    id: 2,
    name: "普通员工",
    code: "common",
    sort: 2,
    status: 1,
    admin: false,
    description: "负责一些基础功能",
    createBy: null,
    createTime: "2025-03-27 11:21:01",
    updateBy: null,
    updateTime: null
  }
];

// 字典数据
export const dictData = [
  {
    id: "01",
    name: "性别",
    code: "gender",
    status: 1,
    description: "这是一个性别字典",
    createTime: "2025-07-01 10:00:00",
    list: [
      { id: "012", name: "女", value: 0, status: 1 },
      { id: "011", name: "男", value: 1, status: 1 },
      { id: "013", name: "未知", value: 2, status: 1 }
    ]
  },
  {
    id: "02",
    name: "状态",
    code: "status",
    status: 1,
    description: "状态字段可以用这个",
    createTime: "2025-07-01 10:00:00",
    list: [
      { id: "021", name: "禁用", value: 0, status: 1 },
      { id: "022", name: "启用", value: 1, status: 1 }
    ]
  },
  {
    id: "03",
    name: "岗位",
    code: "post",
    status: 1,
    description: "岗位字段",
    createTime: "2025-07-01 10:00:00",
    list: [
      { id: "031", name: "总经理", value: 1, status: 1 },
      { id: "032", name: "总监", value: 2, status: 1 },
      { id: "033", name: "人事主管", value: 3, status: 1 },
      { id: "034", name: "开发部主管", value: 4, status: 1 },
      { id: "035", name: "普通职员", value: 5, status: 1 },
      { id: "036", name: "其它", value: 999, status: 1 }
    ]
  },
  {
    id: "04",
    name: "任务状态",
    code: "taskStatus",
    status: 1,
    description: "任务状态字段可以用它",
    createTime: "2025-07-01 10:00:00",
    list: [
      { id: "041", name: "失败", value: 0, status: 1 },
      { id: "042", name: "成功", value: 1, status: 1 }
    ]
  }
];

// 账户数据
export const accountData = [
  {
    id: 1,
    deptId: "100",
    deptName: "研发部门",
    userName: "admin",
    nickName: "超级管理员",
    email: "702080167@qq.com",
    phone: "15888888888",
    sex: 1,
    avatar: "/src/assets/img/my-image.jpg",
    status: 1,
    description: "系统初始用户",
    roles: ["admin"],
    loginIp: "0:0:0:0:0:0:0:1",
    loginDate: "2025-03-31 10:30:59",
    createBy: "admin",
    createTime: "2025-03-19 11:21:01",
    updateBy: null,
    updateTime: null,
    admin: true
  },
  {
    id: 2,
    deptId: "100010101",
    deptName: "研发部门",
    userName: "common",
    nickName: "普通用户",
    email: "702080167@qq.com",
    phone: "15888888888",
    sex: 0,
    avatar: "https://ooo.0x0.ooo/2025/04/10/O0ddJI.jpg",
    status: 1,
    description: "UI组用户",
    roles: ["common"],
    loginIp: "0:0:0:0:0:0:0:1",
    loginDate: "2025-03-31 10:30:59",
    createBy: "admin",
    createTime: "2025-03-19 11:21:01",
    updateBy: null,
    updateTime: null,
    admin: false
  }
];
