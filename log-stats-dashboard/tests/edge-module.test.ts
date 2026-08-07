import { expect, test } from "vitest";
import { toTypeScriptModule } from "../scripts/edge-module.mjs";

test("serializes self-contained HTML as an importable TypeScript string", () => {
  const html = '<!doctype html><script>const text = `value ${1} \\ path`;</script>';
  const moduleSource = toTypeScriptModule(html);

  expect(moduleSource).toBe(`export const dashboardHtml = ${JSON.stringify(html)};\n`);
  expect(moduleSource).not.toContain("String.raw`");
});
