import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";
import { toTypeScriptModule } from "./edge-module.mjs";

const projectRoot = fileURLToPath(new URL("..", import.meta.url));
const htmlPath = resolve(projectRoot, "dist/index.html");
const outputPath = resolve(projectRoot, "../supabase/functions/log-usage-dashboard/dashboard_html.ts");
const html = await readFile(htmlPath, "utf8");

if (/<script\b[^>]*\bsrc\s*=/i.test(html) || /<link\b[^>]*\brel=["']?stylesheet/i.test(html)) {
  throw new Error("Edge dashboard build is not self-contained");
}
if (!/<script\b/i.test(html) || !/<style\b/i.test(html)) {
  throw new Error("Edge dashboard build must contain inline JavaScript and CSS");
}

await writeFile(outputPath, toTypeScriptModule(html), "utf8");
console.log(`Generated ${outputPath}`);
