// THE MECHANICAL FORM OF GAP 1.1.
//
//   node extension/harness/pendingfit.mjs
//
// Exits non-zero when the staged diff does not fit the box that shows it, or
// when the `−`/`+` pair is not inside that box's visible rectangle. `node
// --test` cannot see either: it has no layout, so it cannot tell a diff that is
// rendered from a diff that is rendered 103px below the fold of a 51px scroll
// box with an empty footer underneath. That is exactly what shipped.
//
// It is not part of `npm test` because it needs a browser. Run it on any commit
// that touches the pending strip or the footer's height cascade.
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

// Three panel sizes, because the defect was a height cascade and a height
// cascade is only wrong at some heights. 560 is the narrow layout; 480 is
// shorter than the diff needs, and there the box is allowed to scroll — but it
// must have scrolled the change into view rather than opening on the header.
//
// 760 was added 2026-08-15. This script was the ONLY one in the harness that
// ever left 1100, and it jumped straight from 1100 to 560 — over the whole
// 600–900 band, which is the band `style.css` writes a separate layout for and
// the band in which every Epic 9 affordance turned out to be unusable. 560 is
// the sub-600 reflow and answers a different question; it does not stand in for
// the one in between.
const SIZES = [
  { w: 1100, h: 720 },
  { w: 760, h: 720 },
  { w: 560, h: 720 },
  { w: 1100, h: 480 },
];

const failures = [];
for (const size of SIZES) {
  const page = await browser.newPage();
  await page.setViewport({ width: size.w, height: size.h });
  await page.goto(`${base}?scenario=pending&theme=dark`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true');
  const m = await page.evaluate(() => {
    const diff = document.querySelector('.pending-diff');
    const footer = document.querySelector('.inspector-footer');
    const pending = document.querySelector('.pending');
    const summary = document.querySelector('.pending-summary');
    const box = diff.getBoundingClientRect();
    const rows = [...diff.children].map((c) => {
      const r = c.getBoundingClientRect();
      return { text: c.textContent, cls: c.className, top: r.top, bottom: r.bottom };
    });
    const inBox = (r) => r.top >= box.top - 0.5 && r.bottom <= box.bottom + 0.5;
    const change = rows.filter((r) => /diff-(add|del)$/.test(r.cls));
    return {
      diff: { clientH: diff.clientHeight, scrollH: diff.scrollHeight, top: box.top, bottom: box.bottom },
      footerH: footer.getBoundingClientRect().height,
      pendingH: pending.getBoundingClientRect().height,
      deadFooter: Math.round(footer.getBoundingClientRect().height - pending.getBoundingClientRect().height),
      summary: summary.textContent,
      summaryClipped: summary.scrollWidth > summary.clientWidth,
      headerLines: rows.filter((r) => r.cls.endsWith('diff-meta')).map((r) => r.text),
      changeLines: change.length,
      changeVisible: change.filter(inBox).length,
    };
  });
  await page.close();

  const label = `${size.w}x${size.h}`;
  const say = (ok, what) => {
    console.log(`${ok ? 'ok  ' : 'FAIL'} ${label}  ${what}`);
    if (!ok) failures.push(`${label}: ${what}`);
  };
  say(m.changeLines >= 2, `the strip renders the −/+ pair (${m.changeLines} change lines)`);
  say(
    m.changeVisible === m.changeLines,
    `every change line is inside the visible box (${m.changeVisible}/${m.changeLines})`,
  );
  say(
    m.headerLines.length === 0,
    `no --- / +++ line takes room from the change (${m.headerLines.length})`,
  );
  say(m.deadFooter <= 1, `the footer is not taller than the strip in it (${m.deadFooter}px spare)`);
  say(!m.summaryClipped, `the summary is not truncated ("${m.summary}")`);
  console.log(
    `      diff ${m.diff.clientH}px tall, needs ${m.diff.scrollH}px; footer ${Math.round(m.footerH)}px, strip ${Math.round(m.pendingH)}px`,
  );
}

await browser.close();
server.close();
if (failures.length > 0) {
  console.error(`\n${failures.length} failure(s):\n  ${failures.join('\n  ')}`);
  process.exit(1);
}
console.log('\nthe staged change is visible at every size checked');
