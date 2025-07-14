// 临时注释掉，避免模块解析问题
// import { createProdMockServer } from "vite-plugin-mock/client";

import testModule from "./test/index";
import userModule from "./user/index";
import systemModule from "./system/index";
import fileModule from "./file/index";
import tableModule from "./table/index";
import monitorModule from "./monitor/index";
import dataModule from "./data/index";

export function setupProdMockServer() {
  // createProdMockServer([...testModule, ...userModule, ...systemModule, ...fileModule, ...tableModule, ...monitorModule, ...dataModule]);
  console.log('Mock server setup skipped for now');
}
