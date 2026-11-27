/** 把下载好的 Blob 交给浏览器保存。 */
export function saveBlob(blob: Blob, fileName: string) {
  const href = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = href;
  a.download = fileName;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  // 留一点时间给浏览器开始保存再释放
  setTimeout(() => URL.revokeObjectURL(href), 60_000);
}
