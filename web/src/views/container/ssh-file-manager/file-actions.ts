import type { SftpFileItem } from "@/api/modules/ssh";

/** SFTP 删除前的说明：写清在哪台主机、删的是什么。目录会连同里面的全部内容一起删（后端 RemoveAll）。 */
export function deleteConfirmText(item: Pick<SftpFileItem, "type" | "path">, hostLabel: string): string {
  const where = hostLabel ? `在 ${hostLabel} ` : "";
  return item.type === "d"
    ? `${where}删除目录 ${item.path} 及其全部内容？此操作不可恢复。`
    : `${where}删除文件 ${item.path}？此操作不可恢复。`;
}

/** 新建目录弹窗的标题：写清在哪台主机、建在哪个目录下。 */
export function mkdirTitle(dir: string, hostLabel: string): string {
  return `${hostLabel ? `在 ${hostLabel} 的 ` : "在 "}${dir} 下新建目录`;
}
