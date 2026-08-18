// What a raised surface ACTUALLY renders as, and how far it is from the pane.
//
//   node extension/harness/surfaces.mjs
//
// The token tables in `a11y.ts` say which tokens are paired; this resolves them
// in a real engine — including the translucent ones, which no token comparison
// can do — and prints the WCAG ratio of each surface and each border against
// the pane behind it. That is the number the Light+ value chip failed: 1.00.
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
  } catch {
    res.writeHead(404).end('not found');
  }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}/harness/index.html`;
const browser = await puppeteer.launch({
  executablePath: process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});

/** sRGB channel -> linear, per WCAG 2.x. */
const lin = (c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const luminance = ([r, g, b]) => 0.2126 * lin(r / 255) + 0.7152 * lin(g / 255) + 0.0722 * lin(b / 255);
const ratio = (a, b) => {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
};
/** `rgb(a, b, c)` / `rgba(a, b, c, alpha)` composited over `over`. */
const parse = (value, over) => {
  const n = (value.match(/[\d.]+/g) ?? []).map(Number);
  if (n.length < 3) return null;
  const alpha = n.length > 3 ? n[3] : 1;
  return [0, 1, 2].map((i) => Math.round(n[i] * alpha + over[i] * (1 - alpha)));
};

for (const theme of ['dark', 'light']) {
  const page = await browser.newPage();
  await page.setViewport({ width: 1100, height: 720 });
  await page.goto(`${base}?scenario=service&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true');
  const raw = await page.evaluate(() => {
    const cs = (sel, prop) => {
      const e = document.querySelector(sel);
      return e ? getComputedStyle(e)[prop] : null;
    };
    return {
      pane: cs('.inspector', 'backgroundColor'),
      canvas: cs('.pane-graph', 'backgroundColor') || cs('.app', 'backgroundColor'),
      chipFill: cs('.field-value.field-input', 'backgroundColor'),
      chipBorder: cs('.field-value.field-input', 'borderTopColor'),
      boxFill: cs('.node-box', 'fill'),
      boxStroke: cs('.node-box', 'stroke'),
      // The node kind that used to be drawn on `input-background`, which in
      // Light+ is the editor background — 1.000:1, invisible, and recorded in
      // `RAISED_SURFACES` as live residue rather than fixed. Kept here now that
      // it is fixed, because a ratio nobody prints is a ratio nobody notices
      // going back to 1.
      portFill: cs('.node-port .node-box', 'fill'),
      portStroke: cs('.node-port .node-box', 'stroke'),
    };
  });
  await page.close();

  const pane = parse(raw.pane, [0, 0, 0]);
  const canvas = parse(raw.canvas ?? raw.pane, pane);
  const rows = [
    ['editable chip fill   vs inspector pane', parse(raw.chipFill, pane), pane],
    ['editable chip border vs inspector pane', parse(raw.chipBorder, pane), pane],
    ['node box fill    vs canvas', parse(raw.boxFill, canvas), canvas],
    ['node box stroke  vs canvas', parse(raw.boxStroke, canvas), canvas],
    ['port box fill    vs canvas', parse(raw.portFill, canvas), canvas],
    ['port box stroke  vs canvas', parse(raw.portStroke, canvas), canvas],
  ];
  console.log(`\n${theme === 'dark' ? 'Dark+' : 'Light+'}  pane rgb(${pane})  canvas rgb(${canvas})`);
  for (const [what, colour, behind] of rows) {
    if (colour === null) {
      console.log(`  ${what}: not resolvable`);
      continue;
    }
    console.log(`  ${what.padEnd(36)} rgb(${colour.join(',')})  ${ratio(colour, behind).toFixed(3)}:1`);
  }
}

await browser.close();
server.close();
