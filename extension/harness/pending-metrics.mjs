import http from 'node:http';
import { readFile } from 'node:fs/promises';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import puppeteer from 'puppeteer-core';
const here = path.dirname(fileURLToPath(import.meta.url));
const ext = path.resolve(here, '..');
const T = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json' };
const server = http.createServer(async (req, res) => {
  const rel = decodeURIComponent((req.url || '/').split('?')[0]);
  try { const b = await readFile(path.join(ext, rel.replace(/^\//, ''))); res.writeHead(200, { 'content-type': T[path.extname(rel)] || 'application/octet-stream' }); res.end(b); }
  catch { res.writeHead(404).end('no'); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}/harness/index.html`;
const browser = await puppeteer.launch({ executablePath: process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const page = await browser.newPage();
await page.setViewport({ width: 1100, height: 720 });
await page.goto(`${base}?scenario=pending&theme=dark`, { waitUntil: 'networkidle0' });
await page.waitForFunction('window.__ready === true');
console.log(JSON.stringify(await page.evaluate(() => {
  const box = (s) => { const e = document.querySelector(s); if (!e) return null; const r = e.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), top: Math.round(r.top), bottom: Math.round(r.bottom), scrollH: e.scrollHeight, clientH: e.clientHeight }; };
  const diff = document.querySelector('.pending-diff');
  const lines = diff ? [...diff.children].map((c) => ({ t: c.textContent.slice(0, 46), cls: c.className, top: Math.round(c.getBoundingClientRect().top) })) : [];
  return {
    inspector: box('.inspector'), body: box('.inspector-body'), footer: box('.inspector-footer'),
    pending: box('.pending'), diff: box('.pending-diff'), summary: document.querySelector('.pending-summary')?.textContent,
    summaryClipped: (() => { const e = document.querySelector('.pending-summary'); return e ? e.scrollWidth > e.clientWidth : null; })(),
    lineCount: lines.length, linesVisible: lines.filter((l) => l.top < 720).length, lines,
  };
}), null, 1));
await browser.close(); server.close();
