// Computed-style readings from the rendered webview — the half of "does it look
// like the mockup" that a screenshot cannot be diffed for.
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import puppeteer from 'puppeteer-core';

const here = path.dirname(fileURLToPath(import.meta.url));
const ext = path.resolve(here, '..');
const TYPES = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json' };
const server = http.createServer(async (req, res) => {
  const rel = decodeURIComponent((req.url || '/').split('?')[0]);
  try {
    const body = await readFile(path.join(ext, rel.replace(/^\//, '')));
    res.writeHead(200, { 'content-type': TYPES[path.extname(rel)] || 'application/octet-stream' });
    res.end(body);
  } catch { res.writeHead(404).end('no'); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}/harness/index.html`;
const browser = await puppeteer.launch({
  executablePath: process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});
const page = await browser.newPage();
await page.setViewport({ width: 1100, height: 720 });
await page.goto(`${base}?scenario=service&theme=dark`, { waitUntil: 'networkidle0' });
await page.waitForFunction('window.__ready === true');

const report = await page.evaluate(() => {
  const read = (sel) => {
    const e = document.querySelector(sel);
    if (!e) return { selector: sel, present: false };
    const s = getComputedStyle(e);
    const r = e.getBoundingClientRect();
    return {
      selector: sel, present: true,
      text: (e.textContent || '').trim().slice(0, 60),
      font: s.fontFamily.split(',')[0], size: s.fontSize, weight: s.fontWeight,
      transform: s.textTransform, spacing: s.letterSpacing, color: s.color,
      background: s.backgroundColor, border: s.border, borderBottom: s.borderBottom,
      padding: s.padding, margin: s.margin, w: Math.round(r.width), h: Math.round(r.height),
    };
  };
  const sels = [
    '.pane-header', '.pane-title', '.pane-summary', '.inspector', '.inspector-header',
    '.grp', '.grp-t', '.grp-name', '.field-block', '.node-box', '.inspector-body', '.pending-body', '.prov-link', '.addable-lead', '.field', '.field-key', '.field-value',
    '.prov', '.addable', '.addable-key', '.pill', '.resolution', '.pending', '.pending-head',
    '.pending-diff', '.toolbar', '.legend', '.node', '.node-label', '.node-detail',
  ];
  const styles = sels.map(read);
  const counts = {
    groups: document.querySelectorAll('.grp').length,
    groupsWithNoField: [...document.querySelectorAll('.grp')].filter((g) => g.querySelectorAll('.field').length === 0).length,
    addableBlocks: document.querySelectorAll('.addable').length,
    fields: document.querySelectorAll('.field').length,
    provLines: document.querySelectorAll('.prov').length,
    pills: document.querySelectorAll('.pill').length,
  };
  // Which group headings exist, and in what order.
  const headings = [...document.querySelectorAll('.grp')].map((g) => {
    const t = g.querySelector('.grp-name');
    return t ? t.textContent.trim().replace(/\s+/g, ' ') : '(no title)';
  });
  // Inspector scroll: how much of it is off-screen at 720px.
  const body = document.querySelector('.inspector-body') || document.querySelector('.inspector');
  const scroll = body ? { clientHeight: body.clientHeight, scrollHeight: body.scrollHeight } : null;
  // Pending strip: is the diff body visible?
  const diff = document.querySelector('.pending-diff, .pending pre, .pending .diff');
  const diffBox = diff ? diff.getBoundingClientRect() : null;
  return { styles, counts, headings, scroll, diffBox: diffBox && { h: Math.round(diffBox.height), top: Math.round(diffBox.top) } };
});
console.log(JSON.stringify(report, null, 1));
await browser.close();
server.close();
