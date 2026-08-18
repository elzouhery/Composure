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
  try { const b = await readFile(path.join(ext, rel.replace(/^\//, ''))); res.writeHead(200, { 'content-type': T[path.extname(rel)] || '' }); res.end(b); } catch { res.writeHead(404).end('no'); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}/harness/index.html`;
const browser = await puppeteer.launch({ executablePath: process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });

const page = await browser.newPage();
await page.setViewport({ width: 1100, height: 720 });
await page.goto(`${base}?scenario=dockerfile&theme=dark`, { waitUntil: 'networkidle0' });
await page.waitForFunction('window.__ready === true');
console.log('DOCKERFILE', JSON.stringify(await page.evaluate(() => {
  const rows = [...document.querySelectorAll('.field')].map((f) => {
    const k = f.querySelector('.field-key'), v = f.querySelector('.field-value');
    const r = (e) => e ? { t: e.textContent.slice(0, 40), x: Math.round(e.getBoundingClientRect().left), w: Math.round(e.getBoundingClientRect().width) } : null;
    return { key: r(k), value: r(v) };
  });
  return { rows: rows.slice(0, 4).concat(rows.slice(-4)), total: rows.length };
}), null, 1));
await page.close();

const p2 = await browser.newPage();
await p2.setViewport({ width: 1100, height: 720 });
await p2.goto(`${base}?scenario=service&theme=dark`, { waitUntil: 'networkidle0' });
await p2.waitForFunction('window.__ready === true');
console.log('CHROME-ABOVE-CANVAS', JSON.stringify(await p2.evaluate(() => {
  const h = (s) => { const e = document.querySelector(s); return e ? Math.round(e.getBoundingClientRect().height) : 0; };
  const canvas = document.querySelector('.canvas').getBoundingClientRect();
  return {
    paneHeader: h('.pane-header'), toolbar: h('.toolbar'), addForm: h('.add-form'),
    legend: h('.legend'), status: h('.toolbar-status'), note: h('.toolbar-note'),
    canvasTop: Math.round(canvas.top), canvasH: Math.round(canvas.height),
    graphPaneH: Math.round(document.querySelector('.pane-graph').getBoundingClientRect().height),
  };
}), null, 1));
await browser.close(); server.close();
