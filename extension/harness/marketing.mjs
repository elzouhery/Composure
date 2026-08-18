// The README's screenshots, cropped for a marketplace column.
//
//   node extension/harness/marketing.mjs      # -> extension/media/*.png
//
// Same rig as shoot.mjs: the SHIPPED webview bundle, driven by the SHIPPED host
// adapters, against answers captured from the SHIPPED core by capture.mjs.
// Nothing here paints anything the product does not paint — every crop is a
// rectangle of a real render, taken from a real element's own box.
//
// Light theme throughout: the marketplace page is white, and a dark shot on a
// white page reads as a different product. Requires Google Chrome (CHROME
// overrides the path); puppeteer-core is a devDependency and the browser is not
// bundled.
import http from 'node:http';
import { readFile, mkdir } from 'node:fs/promises';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import puppeteer from 'puppeteer-core';

const here = path.dirname(fileURLToPath(import.meta.url));
const ext = path.resolve(here, '..');
const out = process.env.SHOTS ? path.resolve(process.env.SHOTS) : path.join(ext, 'media');
const CHROME =
  process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';

const TYPES = {
  '.html': 'text/html',
  '.js': 'text/javascript',
  '.css': 'text/css',
  '.json': 'application/json',
};
const server = http.createServer(async (req, res) => {
  const rel = decodeURIComponent((req.url || '/').split('?')[0]);
  const file = path.join(ext, rel === '/' ? 'harness/index.html' : rel.replace(/^\//, ''));
  try {
    const body = await readFile(file);
    res.writeHead(200, {
      'content-type': TYPES[path.extname(file)] || 'application/octet-stream',
    });
    res.end(body);
  } catch {
    res.writeHead(404).end('not found');
  }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}/harness/index.html`;
await mkdir(out, { recursive: true });

const browser = await puppeteer.launch({ executablePath: CHROME, headless: true });

/**
 * One capture. `clip` is either a rectangle in CSS pixels or a function of the
 * page returning one, so a crop can be derived from an element's own box rather
 * than from a number someone typed and nobody re-measured.
 */
async function shot(name, { scenario, width = 1100, height = 700, theme = 'light', act, clip }) {
  const page = await browser.newPage();
  await page.setViewport({ width, height, deviceScaleFactor: 2 });
  await page.goto(`${base}?scenario=${scenario}&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });
  if (act) await act(page);
  await new Promise((r) => setTimeout(r, 250));
  const rect = typeof clip === 'function' ? await clip(page) : clip;
  await page.screenshot({ path: path.join(out, `${name}.png`), clip: rect });
  console.log(name, rect ? JSON.stringify(rect) : `${width}x${height}`);
  await page.close();
}

/** The box of the first element matching `sel`, padded, clamped to the viewport. */
const around = (sel, pad = 8) => async (page) =>
  page.evaluate(
    ([s, p]) => {
      const e = document.querySelector(s);
      if (!e) throw new Error(`no element matches ${s}`);
      const r = e.getBoundingClientRect();
      const x = Math.max(0, Math.round(r.x - p));
      const y = Math.max(0, Math.round(r.y - p));
      return {
        x,
        y,
        width: Math.min(innerWidth - x, Math.round(r.width + p * 2)),
        height: Math.min(innerHeight - y, Math.round(r.height + p * 2)),
      };
    },
    [sel, pad],
  );

/* ---------------------------------------------------------------------------
 * 1. The stack, with a service selected. The whole panel, because the claim the
 *    opening line makes is that both halves are one view of one file.
 * ------------------------------------------------------------------------ */
await shot('stack-and-inspector', {
  scenario: 'service',
  width: 1100,
  height: 700,
});

/* 2. Provenance: two values whose origin is a merge, each naming the file, the
 *    line, and the anchor they were merged from. */
await shot('provenance', {
  scenario: 'service',
  height: 760,
  clip: async (page) => {
    // The `networks` and `restart` rows: both resolved through `<<: *defaults`,
    // so both print the merge step as well as the line.
    await page.evaluate(() => {
      const rows = [...document.querySelectorAll('.field-key')];
      const nets = rows.find((e) => e.textContent.trim() === 'networks');
      const body = document.querySelector('.inspector-body');
      body.scrollTop +=
        nets.getBoundingClientRect().top - body.getBoundingClientRect().top - 40;
    });
    await new Promise((r) => setTimeout(r, 100));
    return page.evaluate(() => {
      const col = document.querySelector('.inspector').getBoundingClientRect();
      return { x: Math.round(col.x), y: 27, width: Math.round(col.width), height: 296 };
    });
  },
});

/* 3. The diagnostic, on the field that caused it. */
await shot('diagnostic', {
  scenario: 'service',
  height: 760,
  clip: around('.field-block:has(.is-unresolved)', 10),
});

/* 4. Available, not set — every key the specification permits here and the file
 *    does not use, grouped, one click from being a field. */
await shot('available-not-set', {
  scenario: 'service',
  height: 860,
  act: async (page) => {
    await page.evaluate(() => {
      const blocks = document.querySelectorAll('.addable');
      const last = blocks[blocks.length - 1];
      last.scrollIntoView({ block: 'end' });
    });
    await new Promise((r) => setTimeout(r, 120));
  },
  clip: async (page) =>
    page.evaluate(() => {
      const blocks = document.querySelectorAll('.addable');
      const e = blocks[blocks.length - 1];
      const r = e.getBoundingClientRect();
      const col = document.querySelector('.inspector').getBoundingClientRect();
      return {
        x: Math.round(col.x),
        y: Math.max(27, Math.round(r.y - 10)),
        width: Math.round(col.width),
        height: Math.min(innerHeight - Math.max(27, r.y - 10), Math.round(r.height + 20)),
      };
    }),
});

/* 5. The pending diff, before anything is written. The staged field says what
 *    the file still holds; the strip below says what pressing Save would do. */
await shot('pending-diff', {
  scenario: 'pending',
  height: 640,
  clip: async (page) =>
    page.evaluate(() => {
      const col = document.querySelector('.inspector').getBoundingClientRect();
      return { x: Math.round(col.x), y: 27, width: Math.round(col.width), height: innerHeight - 27 };
    }),
});

/* 6. The Dockerfile view: one group per stage, instructions in file order, each
 *    carrying its line. Narrower than the compose pane on purpose — the form is
 *    one column and 1100px of it is mostly empty rule. */
await shot('dockerfile-stages', {
  scenario: 'dockerfile',
  width: 720,
  height: 620,
});

/* 7. Docker Hub — the one thing that leaves the machine, driven the way a reader
 *    drives it. The control really posts `searchImage` through
 *    `acquireVsCodeApi()`; the harness reads the token it used out of
 *    `window.__posted` and delivers a real `imageSearch` message back, so this is
 *    a state the wire can produce rather than one injected into the view.
 *
 *    The Epic 8 upgrade pill is deliberately NOT shot: its fixture is a fixed
 *    nginx answer pinned to `services.web.image`, which holds a ghcr.io
 *    reference — a true picture of the harness and a false one of the product.
 */
await shot('image-search', {
  scenario: 'service',
  height: 760,
  act: async (page) => {
    await page.evaluate(() => {
      const input = document.querySelector('[data-field="services.web.image"]');
      input.focus();
      input.value = 'postgres';
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await new Promise((r) => setTimeout(r, 500)); // past the control's debounce
    await page.evaluate(() => {
      const ask = [...window.__posted].reverse().find((m) => m.type === 'searchImage');
      if (!ask) throw new Error('the search control never sent a searchImage message');
      window.postMessage(
        {
          type: 'imageSearch',
          token: ask.token,
          answer: {
            query: ask.query,
            state: 'ok',
            message: '',
            results: [
              { name: 'postgres', description: 'The PostgreSQL object-relational database system', stars: 14979, pulls_display: '1B+', official: true, badge: 'official', architectures: ['amd64', 'arm64'] },
              { name: 'dhi/postgres', description: 'Docker Hardened Image for PostgreSQL', stars: 0, pulls_display: '5M+', official: false, badge: 'hardened', architectures: ['amd64', 'arm64'] },
              { name: 'bitnami/postgresql', description: 'Bitnami container image for PostgreSQL', stars: 1200, pulls_display: '500M+', official: false, badge: 'verified_publisher', architectures: ['amd64', 'arm64'] },
            ],
          },
        },
        '*',
      );
    });
    await new Promise((r) => setTimeout(r, 200));
  },
  clip: async (page) =>
    page.evaluate(() => {
      // The largest match: the class is on both the popup and a zero-sized
      // anchor, and the anchor is first in document order.
      const boxes = [...document.querySelectorAll('.imagesearch-popup')]
        .map((e) => e.getBoundingClientRect())
        .sort((a, b) => b.height - a.height);
      const r = boxes[0];
      if (!r || r.height < 40) throw new Error('the search popup never opened');
      const col = document.querySelector('.inspector').getBoundingClientRect();
      const y = Math.max(27, Math.round(r.y - 60));
      return {
        x: Math.round(col.x),
        y,
        width: Math.round(col.width),
        height: Math.min(innerHeight - y, Math.round(r.height + 80)),
      };
    }),
});

await browser.close();
server.close();
console.log('images in', out);
