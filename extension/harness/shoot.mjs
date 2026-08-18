// Renders the shipped webview bundle outside VS Code and screenshots it.
//
//   node harness/capture.mjs   # real core answers -> harness/fixtures/core.json
//   node harness/shoot.mjs     # -> images in shots-render/ at the repo root
//
// Requires Google Chrome installed (puppeteer-core is a devDependency; the
// browser is not bundled). CHROME env overrides the executable path.
import http from 'node:http';
import { readFile, mkdir } from 'node:fs/promises';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import puppeteer from 'puppeteer-core';

const here = path.dirname(fileURLToPath(import.meta.url));
const ext = path.resolve(here, '..');
const repo = path.resolve(ext, '..');
// SHOTS overrides the destination, so a before/after pair can be shot into a
// scratch directory. The default is a `shots-*` directory at the repository
// root, matching epic9.mjs and system.mjs and covered by the `/shots-*/` ignore
// rule -- it used to default outside this repository entirely, which is how a
// run in this working copy grew a tree that did not belong to it.
const shots = process.env.SHOTS
  ? path.resolve(process.env.SHOTS)
  : path.join(repo, 'shots-render');
const CHROME =
  process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';

const TYPES = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json' };
const server = http.createServer(async (req, res) => {
  const rel = decodeURIComponent((req.url || '/').split('?')[0]);
  const file = path.join(ext, rel === '/' ? 'harness/index.html' : rel.replace(/^\//, ''));
  try {
    const body = await readFile(file);
    res.writeHead(200, { 'content-type': TYPES[path.extname(file)] || 'application/octet-stream' });
    res.end(body);
  } catch {
    res.writeHead(404).end('not found');
  }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}/harness/index.html`;
await mkdir(shots, { recursive: true });

const browser = await puppeteer.launch({ executablePath: CHROME, headless: true, args: ['--force-device-scale-factor=2'] });

/** One capture. `act` runs in the page after the messages have landed. */
async function shot(name, { scenario, theme = 'dark', width = 1100, height = 720, act } = {}) {
  const page = await browser.newPage();
  await page.setViewport({ width, height, deviceScaleFactor: 2 });
  await page.goto(`${base}?scenario=${scenario}&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });
  if (act) await act(page);
  await new Promise((r) => setTimeout(r, 200));
  await page.screenshot({ path: path.join(shots, `${name}.png`) });
  const metrics = await page.evaluate(() => {
    const q = (s) => document.querySelector(s);
    const box = (s) => { const e = q(s); if (!e) return null; const r = e.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height) }; };
    const cs = (s, p) => { const e = q(s); return e ? getComputedStyle(e)[p] : null; };
    return {
      graphPane: box('.pane-graph'),
      inspector: box('.inspector'),
      paneHeaders: document.querySelectorAll('.pane-header').length,
      groupTitles: [...document.querySelectorAll('.group-title, .group > h3, .group-name')].map((e) => e.textContent),
      fieldRowHeight: box('.field'),
      fieldKeyWidth: box('.field-key'),
      groupTitleStyle: {
        color: cs('.group-title', 'color'),
        fontSize: cs('.group-title', 'fontSize'),
        letterSpacing: cs('.group-title', 'letterSpacing'),
        textTransform: cs('.group-title', 'textTransform'),
        borderBottom: cs('.group-title', 'borderBottom'),
      },
      nodeCount: document.querySelectorAll('.node').length,
      addableCount: document.querySelectorAll('.addable-key').length,
    };
  });
  console.log(name, JSON.stringify(metrics));
  await page.close();
}

await shot('01-stack-nothing-selected', { scenario: 'stack' });
await shot('02-service-selected', { scenario: 'service' });
await shot('02b-service-selected-light', { scenario: 'service', theme: 'light' });
await shot('03-inspector-scrolled-available', {
  scenario: 'service',
  act: (p) => p.evaluate(() => {
    const blocks = document.querySelectorAll('.addable');
    blocks[blocks.length - 1].scrollIntoView({ block: 'end' });
  }),
});
await shot('04b-pending-diff-closeup', {
  scenario: 'pending',
  width: 1100, height: 720,
  act: (p) => p.evaluate(() => {
    const f = document.querySelector('.inspector-footer');
    f.style.outline = '2px dashed magenta';
    document.querySelector('.pending-diff').style.outline = '2px dashed cyan';
  }),
});
await shot('04-pending-diff', { scenario: 'pending' });
await shot('05-dockerfile-stage-form', { scenario: 'dockerfile' });
await shot('05b-dockerfile-bottom', {
  scenario: 'dockerfile',
  act: (p) => p.evaluate(() => {
    const blocks = document.querySelectorAll('.addable');
    blocks[blocks.length - 1].scrollIntoView({ block: 'end' });
  }),
});
await shot('06-empty-stack', { scenario: 'empty' });
await shot('07-narrow-560', { scenario: 'service', width: 560, height: 720 });
await shot('08-large-stack-graph', { scenario: 'large', width: 1100, height: 720 });
await shot('09-stack-tall-inspector', { scenario: 'service', width: 1100, height: 1400 });

/* -------------------------------------------------------------------------
 * Epic 8 — the upgrade pill and the Docker Hub search, in both default themes.
 *
 * Both themes for both, because the pill takes the WARNING ink and the popup is
 * a raised surface: those are the two things that went invisible in Light+ the
 * last time this harness was pointed at a new control.
 * ---------------------------------------------------------------------- */

/** Scrolls the image field into view, so the pill is in the frame. */
const showImage = (p) =>
  p.evaluate(() => {
    const pill = document.querySelector('.pill-upgrade') ?? document.querySelector('.image-note');
    if (pill) pill.scrollIntoView({ block: 'center' });
  });

await shot('10-upgrade-pill', { scenario: 'upgrade', act: showImage });
await shot('10b-upgrade-pill-light', { scenario: 'upgrade', theme: 'light', act: showImage });
await shot('11-rate-limited', { scenario: 'ratelimited', act: showImage });
await shot('11b-rate-limited-light', { scenario: 'ratelimited', theme: 'light', act: showImage });
await shot('12-dockerfile-upgrade-pill', { scenario: 'dockerfile-upgrade' });
await shot('12b-dockerfile-upgrade-pill-light', {
  scenario: 'dockerfile-upgrade',
  theme: 'light',
});

/**
 * The search popup, driven the way a reader drives it.
 *
 * Nothing is faked past the boundary: the control really sends `searchImage`
 * through `acquireVsCodeApi().postMessage`, the harness reads the TOKEN it
 * actually used out of `window.__posted`, and the answer is delivered as a real
 * `imageSearch` message. A shot that injected results straight into the view
 * would photograph a state the wire cannot produce.
 */
const openSearch = async (p) => {
  await p.evaluate(() => {
    const input = document.querySelector('[data-field="services.web.image"]');
    input.focus();
    input.value = 'postgres';
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
  // The control debounces before it asks. Waiting past it is the point.
  await new Promise((r) => setTimeout(r, 500));
  await p.evaluate(() => {
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
  await new Promise((r) => setTimeout(r, 150));
  await p.evaluate(() => {
    document.querySelector('.imagesearch-popup')?.scrollIntoView({ block: 'center' });
  });
};

await shot('13-image-search', { scenario: 'service', act: openSearch });
await shot('13b-image-search-light', { scenario: 'service', theme: 'light', act: openSearch });

await browser.close();
server.close();
console.log('shots in', shots);
