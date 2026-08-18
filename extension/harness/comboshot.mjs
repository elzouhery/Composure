// The value list, OPEN, on a field that already holds one of its values.
//
// This is the instrument for the defect the owner reported in story 7.9:
// `restart` is set to `unless-stopped`, and the `<datalist>` that shipped
// filtered its options against the field's own text, so the popup contained one
// entry — the value already on screen — while the other three were links under
// the field.
//
// A test can assert the tree. Only this can assert the PICTURE: that four
// options are painted, that each has a non-zero box, that none is clipped away
// by a stylesheet, and what the whole thing looks like in both default themes.
// A datalist could not be photographed at all, which is half of why it went.
//
//   SHOTS=/tmp/somewhere node extension/harness/comboshot.mjs
//
// Prints one JSON line per capture and exits 1 if any value the core reported
// is not visibly on screen.
import http from 'node:http';
import { readFile, mkdir } from 'node:fs/promises';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import puppeteer from 'puppeteer-core';

const here = path.dirname(fileURLToPath(import.meta.url));
const ext = path.resolve(here, '..');
const repo = path.resolve(ext, '..');
// SHOTS overrides the destination. The default is a `shots-*` directory at
// the repository root, matching epic9.mjs and system.mjs and covered by the `/shots-*/` ignore
// rule -- it used to default outside this repository entirely, which is how a
// run in this working copy grew a tree that did not belong to it.
const shots = process.env.SHOTS
  ? path.resolve(process.env.SHOTS)
  : path.join(repo, 'shots-combo');
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

const PATH = 'services.web.restart';
/** What the core reports for `restart`, in the specification's own order. */
const EXPECTED = ['no', 'always', 'on-failure', 'unless-stopped'];

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: true,
  args: ['--force-device-scale-factor=2'],
});

let failed = 0;

/**
 * Two widths, because `popup.x === field.x` is a property of both bands and was
 * only ever measured in one.
 *
 * 1100 is the width the captures are taken at and stays the only width that
 * writes a PNG — a second set of narrow screenshots is not a deliverable, and
 * the assertion is a number rather than a picture.
 *
 * 760 is the middle of the 600–900 band the stylesheet designs a separate
 * layout for (see `epic9.mjs`, same two widths and the same reasoning). The
 * popup hangs off the key column, that band has no key column, and at 760 the
 * popup measured x = 556 against a field at x = 468 — 88px to the right of the
 * field it belongs to, with no check anywhere that would have said so.
 */
const WIDTHS = [1100, 760];

for (const width of WIDTHS)
for (const theme of ['dark', 'light']) {
  const page = await browser.newPage();
  await page.setViewport({ width, height: 820, deviceScaleFactor: 2 });
  await page.goto(`${base}?scenario=service&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });

  const measured = await page.evaluate((p) => {
    const field = document.querySelector(`[data-field="${p}"]`);
    const toggle = document.querySelector(`.combo-toggle[data-for="${p}"]`);
    if (!field || !toggle) {
      // The BEFORE state. Photographed anyway: the row of links under the field
      // is what the owner rejected, and a before/after pair needs both halves.
      if (field) field.scrollIntoView({ block: 'center' });
      return {
        error: 'no combobox on the field',
        field: !!field,
        toggle: !!toggle,
        fieldValue: field ? field.value : null,
        boundList: field ? field.getAttribute('list') : null,
        linksUnderField: document.querySelectorAll('.allowed-value, .allowed .addable-key').length,
        linkTexts: [...document.querySelectorAll('.allowed-value')].map((e) => e.textContent),
        datalists: document.querySelectorAll('datalist').length,
        selects: document.querySelectorAll('select').length,
      };
    }
    field.scrollIntoView({ block: 'center' });
    toggle.click();
    const popup = document.querySelector(`.combo-popup[data-for="${p}"]`);
    const box = (e) => {
      const r = e.getBoundingClientRect();
      return { x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) };
    };
    // VISIBLE, not merely present: a box with area, a painted display, and
    // inside the popup's own scroll viewport. Anything a stylesheet could use
    // to reduce the list back to one entry has to show up here.
    const popupRect = popup.getBoundingClientRect();
    const options = [...popup.querySelectorAll('[role="option"]')].map((o) => {
      const cs = getComputedStyle(o);
      const r = o.getBoundingClientRect();
      return {
        value: o.dataset.value,
        text: o.querySelector('.combo-text').textContent,
        selected: o.getAttribute('aria-selected'),
        box: box(o),
        display: cs.display,
        visibility: cs.visibility,
        opacity: cs.opacity,
        visible:
          r.width > 0 &&
          r.height > 0 &&
          cs.display !== 'none' &&
          cs.visibility !== 'hidden' &&
          Number(cs.opacity) > 0 &&
          r.bottom > popupRect.top &&
          r.top < popupRect.bottom,
      };
    });
    return {
      fieldValue: field.value,
      expanded: field.getAttribute('aria-expanded'),
      controls: field.getAttribute('aria-controls'),
      activedescendant: field.getAttribute('aria-activedescendant'),
      popupBox: box(popup),
      fieldBox: box(field),
      popupBackground: getComputedStyle(popup).backgroundColor,
      paneBackground: getComputedStyle(document.querySelector('.inspector-body')).backgroundColor,
      options,
      // The rejected design, counted: chips under the field.
      linksUnderField: document.querySelectorAll('.allowed-value, .allowed .addable-key').length,
      datalists: document.querySelectorAll('datalist').length,
      selects: document.querySelectorAll('select').length,
    };
  }, PATH);

  await new Promise((r) => setTimeout(r, 150));
  const name = `10-allowed-values-open-${theme}@${width}`;
  // The captures are the wide band's; the narrow run asserts geometry only.
  if (width === WIDTHS[0]) {
    await page.screenshot({ path: path.join(shots, `10-allowed-values-open-${theme}.png`) });
  }

  const visible = (measured.options || []).filter((o) => o.visible).map((o) => o.text);
  const ok =
    !measured.error &&
    JSON.stringify(visible) === JSON.stringify(EXPECTED) &&
    measured.linksUnderField === 0 &&
    measured.datalists === 0 &&
    measured.selects === 0 &&
    // The popup starts at the value column, not at the key column.
    measured.popupBox?.x === measured.fieldBox?.x &&
    // …and it is a surface, not the pane showing through.
    measured.popupBackground !== measured.paneBackground;
  if (!ok) failed++;
  console.log(`${name} ${ok ? 'OK' : 'FAILED'} ${JSON.stringify({ ...measured, visible })}`);
  await page.close();
}

await browser.close();
server.close();
console.log(failed === 0 ? `\nall ${EXPECTED.length} values are visibly on screen in both themes` : `\n${failed} capture(s) failed`);
process.exit(failed === 0 ? 0 : 1);
