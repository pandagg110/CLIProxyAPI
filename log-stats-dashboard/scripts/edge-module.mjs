export function toTypeScriptModule(html) {
  return `export const dashboardHtml = ${JSON.stringify(html)};\n`;
}
