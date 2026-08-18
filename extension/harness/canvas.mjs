// The graph pane's APPEARANCE, in numbers — instrument, and CHECK.
//
//   node harness/canvas.mjs [scenario] [theme]   # dump
//   node harness/canvas.mjs --check              # exits 1
//
// `graphmetrics.ts` answers "is the picture legible" — crossings, occlusion,
// label collisions. It runs headless against the layout module and never paints
// anything, so it cannot see a card that does not read as a card, a name that
// is the same weight as the line under it, or 27px of empty box below the last
// word. Those are the differences the owner reacted to on 2026-08-13 —
// *"still the UI looks different than the mockup"* — and they are all resolved
// colours and painted boxes, which is a browser's job.
//
// Every assertion below is a RENDERED fact: a resolved fill, a measured text
// bounding box, a real element's tab index. None of them is "a CSS rule
// exists" — twenty-one checks that could not fail have shipped in this
// repository already.
//
// Design of record for the numbers: `mockups/directions-3.html:288–310`, the
// Direction A canvas — a 188×40 card, 10px inset, an 11px name, a 9px detail
// line one step below it, a badge at the top right, and no dead space.
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
const browser = await puppeteer.launch({
  executablePath: process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});

async function measure({ scenario = 'stack', theme = 'dark', width = 1100, height = 720 } = {}) {
  const page = await browser.newPage();
  await page.setViewport({ width, height });
  await page.goto(`${base}?scenario=${scenario}&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });
  const out = await page.evaluate(() => {
    const cs = (el, p) => (el ? getComputedStyle(el)[p] : null);
    const one = (sel) => document.querySelector(sel);

    /** The painted geometry of one node, in the node's OWN coordinates. */
    const nodeFacts = (group) => {
      const box = group.querySelector('.node-box');
      const rect = box.getBBox();
      // Where the last painted glyph of this node ends, measured on the text
      // itself rather than assumed from a constant.
      let inkBottom = -Infinity;
      let inkTop = Infinity;
      let inkLeft = Infinity;
      for (const t of group.querySelectorAll('text')) {
        if (t.classList.contains('node-badge-count')) continue;
        const b = t.getBBox();
        if (b.width === 0 && b.height === 0) continue;
        inkBottom = Math.max(inkBottom, b.y + b.height);
        inkTop = Math.min(inkTop, b.y);
        inkLeft = Math.min(inkLeft, b.x);
      }
      const label = group.querySelector('.node-label');
      const detail = group.querySelector('.node-detail');
      return {
        id: group.dataset.id,
        kind: group.dataset.kind,
        w: +rect.width.toFixed(1),
        h: +rect.height.toFixed(1),
        rx: cs(box, 'rx'),
        fill: cs(box, 'fill'),
        stroke: cs(box, 'stroke'),
        strokeWidth: cs(box, 'strokeWidth'),
        // The three insets a card is read by: text from the left edge, the
        // first baseline from the top, and the gap under the last line.
        padLeft: Number.isFinite(inkLeft) ? +inkLeft.toFixed(1) : null,
        padTop: Number.isFinite(inkTop) ? +inkTop.toFixed(1) : null,
        deadSpace: Number.isFinite(inkBottom) ? +(rect.height - inkBottom).toFixed(1) : null,
        label: label
          ? { fs: cs(label, 'fontSize'), fw: cs(label, 'fontWeight'), fill: cs(label, 'fill'), y: +label.getBBox().y.toFixed(1) }
          : null,
        detail: detail && detail.textContent
          ? { fs: cs(detail, 'fontSize'), fw: cs(detail, 'fontWeight'), fill: cs(detail, 'fill'), y: +detail.getBBox().y.toFixed(1) }
          : null,
      };
    };

    const nodes = [...document.querySelectorAll('.node')].map(nodeFacts);
    // One representative per kind, so the dump stays readable on 771 nodes.
    const byKind = {};
    for (const n of nodes) byKind[n.kind] ??= n;

    const edges = [...document.querySelectorAll('.edge')].map((e) => ({
      kind: (e.getAttribute('class') || '').replace('edge edge-', ''),
      stroke: cs(e, 'stroke'),
      strokeWidth: cs(e, 'strokeWidth'),
      opacity: cs(e, 'opacity'),
      dash: cs(e, 'strokeDasharray'),
    }));

    const plate = one('.edge-label-plate');
    const labelText = one('.edge-label-text');
    const badge = one('.node-badge');
    const badgeCount = one('.node-badge-count');

    const rectOf = (sel) => {
      const e = one(sel);
      if (!e) return null;
      const r = e.getBoundingClientRect();
      return { x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) };
    };

    // The auto-arrange control, as the reader's keyboard finds it.
    const arrange = [...document.querySelectorAll('.toolbar button')].find(
      (b) => b.dataset.control === 'arrange',
    );

    return {
      canvas: cs(one('.canvas'), 'backgroundColor'),
      nodeCount: nodes.length,
      kinds: byKind,
      // Every service card, so ragged heights inside one stack are visible.
      serviceHeights: nodes.filter((n) => n.kind === 'service').map((n) => n.h),
      serviceDead: nodes.filter((n) => n.kind === 'service').map((n) => n.deadSpace),
      edges: edges,
      plate: plate
        ? { fill: cs(plate, 'fill'), stroke: cs(plate, 'stroke'), strokeWidth: cs(plate, 'strokeWidth'), opacity: cs(plate, 'opacity'), h: +plate.getBBox().height.toFixed(1) }
        : null,
      edgeLabel: labelText ? { fs: cs(labelText, 'fontSize'), fill: cs(labelText, 'fill') } : null,
      badge: badge ? { r: badge.getAttribute('r'), fill: cs(badge, 'fill') } : null,
      badgeCount: badgeCount ? { fs: cs(badgeCount, 'fontSize'), fill: cs(badgeCount, 'fill'), fw: cs(badgeCount, 'fontWeight') } : null,
      chrome: {
        pane: rectOf('.pane-graph'),
        header: rectOf('.pane-header'),
        toolbar: rectOf('.toolbar'),
        legend: rectOf('.legend'),
        canvasBox: rectOf('.canvas'),
      },
      arrange: arrange
        ? {
            tag: arrange.tagName,
            type: arrange.type,
            text: (arrange.textContent || '').trim(),
            name: arrange.getAttribute('aria-label'),
            disabled: arrange.disabled,
            ariaDisabled: arrange.getAttribute('aria-disabled'),
            tabindex: arrange.getAttribute('tabindex'),
            // Same surface as the controls beside it, or it reads as a
            // different kind of thing.
            background: cs(arrange, 'backgroundColor'),
            fontSize: cs(arrange, 'fontSize'),
          }
        : null,
    };
  });
  await page.close();
  return out;
}

/**
 * Auto-arrange, driven with a real mouse.
 *
 * The fake-DOM suite proves the wiring; this proves the PICTURE. It drags a
 * node with the browser's own input — synthetic `PointerEvent`s cannot be used,
 * because `setPointerCapture` rejects a pointer id the browser has never seen
 * and the drag would silently never start — then presses the control and reads
 * the transform back off the drawn element.
 */
async function arrangeRoundTrip({ theme = 'dark', width = 1100, height = 720 } = {}) {
  const page = await browser.newPage();
  await page.setViewport({ width, height });
  await page.goto(`${base}?scenario=stack&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });

  const target = '.node[data-kind="service"]';
  const before = await page.$eval(target, (e) => e.getAttribute('transform'));
  const box = await page.$eval(target, (e) => {
    const r = e.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  });
  await page.mouse.move(box.x, box.y);
  await page.mouse.down();
  await page.mouse.move(box.x + 90, box.y + 70, { steps: 8 });
  await page.mouse.up();
  const dragged = await page.$eval(target, (e) => e.getAttribute('transform'));

  const enabled = await page.$eval('[data-control="arrange"]', (b) =>
    b.getAttribute('aria-disabled'),
  );
  await page.click('[data-control="arrange"]');
  const after = await page.$eval(target, (e) => e.getAttribute('transform'));
  const posted = await page.evaluate(() => JSON.stringify(window.__posted.slice(-4)));
  const status = await page.$eval('.toolbar-status', (e) => e.textContent.trim());
  await page.close();
  return { before, dragged, after, enabled, posted: JSON.parse(posted), status };
}

/* ---- colour arithmetic, on RESOLVED paint ------------------------------ */

const rgb = (v) => (v ?? '').match(/[\d.]+/g)?.map(Number) ?? null;
const lin = (c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const lum = ([r, g, b]) => 0.2126 * lin(r / 255) + 0.7152 * lin(g / 255) + 0.0722 * lin(b / 255);

/**
 * `a` composited over `b`, honouring alpha and an element `opacity`.
 *
 * Load-bearing, not a nicety: both default themes ship `panel.border` as
 * `rgba(128,128,128,0.35)`, which as a bare triple measures 3.9:1 against white
 * and as painted measures 1.34:1. A check that ignored alpha would have vouched
 * for the invisible network capsule this pass exists to fix.
 */
function over(a, b, opacity = 1) {
  const [p, q] = [rgb(a), rgb(b)];
  if (!p || !q) return null;
  const alpha = (p.length > 3 ? p[3] : 1) * opacity;
  return [0, 1, 2].map((i) => alpha * p[i] + (1 - alpha) * q[i]);
}
function ratio(a, b, opacity = 1) {
  const front = over(a, b, opacity);
  const back = rgb(b);
  if (!front || !back) return null;
  const [x, y] = [lum(front), lum(back)].sort((m, n) => n - m);
  return (x + 0.05) / (y + 0.05);
}

/**
 * The width `--check` measures at. 1100 by default, so nothing about the gate
 * moves; `--width 760` drives the same checks through the 600–900 band the
 * stylesheet designs a separate layout for. Added 2026-08-15: no scenario in
 * this harness ran below 900px, which is how every Epic 9 affordance came to be
 * unusable in that band with 23 checks green. See `epic9.mjs` for why 760.
 */
const CHECK_WIDTH = Number(
  process.argv.includes('--width') ? process.argv[process.argv.indexOf('--width') + 1] : 1100,
);

let failed = 0;
const check = (ok, label, detail = '') => {
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${label}${detail ? `  ${detail}` : ''}`);
  if (!ok) failed++;
};

if (process.argv.includes('--check')) {
  for (const theme of ['dark', 'light']) {
    const m = await measure({ scenario: 'stack', theme, width: CHECK_WIDTH });
    const at = `stack/${theme}`;

    // 1. THE CARD IS A CARD. The mockup's node is a filled rectangle on a
    // darker canvas with a visible edge. A fill that resolves to the canvas
    // colour behind a hairline is the "plain grey rectangles" reading, and it
    // is a LIGHT-theme failure that a token comparison cannot see: two
    // different `--vscode-*` names routinely resolve to the same white.
    for (const [kind, n] of Object.entries(m.kinds)) {
      const r = ratio(n.fill, m.canvas);
      const edge = ratio(n.stroke, m.canvas);
      check(
        (r !== null && r > 1.02) || (edge !== null && edge > 1.6),
        `${at}  a ${kind} node reads against the canvas`,
        `fill ${n.fill} = ${r === null ? '?' : r.toFixed(3)}:1, stroke ${n.stroke} = ${edge === null ? '?' : edge.toFixed(3)}:1`,
      );
    }

    // 2. NO DEAD SPACE. A service card used to stand at a 94px floor while its
    // content ended at 67 — 27px of empty box under every one-line node, which
    // is what made the canvas read as loose and unlike the mockup's 40px card.
    // Measured on the glyphs, so it cannot be satisfied by a constant.
    const worst = Math.max(...m.serviceDead);
    check(
      worst <= 16,
      `${at}  no service card carries dead space under its last line`,
      `worst ${worst}px of ${m.serviceHeights.length} cards, heights ${[...new Set(m.serviceHeights)].join(',')}`,
    );

    // 3. THE NAME LEADS. The mockup's card is a title over a caption — 11px
    // full ink over 9px dim. Ink alone does not carry it in Light+, where
    // `foreground` and `descriptionForeground` measure 5.13:1 and 4.32:1
    // against the card and are indistinguishable at this size, so BOTH
    // remaining axes are asserted: bigger and heavier. The weight half failed
    // on the previous bundle (400 against 400) in both themes.
    const svc = m.kinds.service;
    const nameSize = parseFloat(svc.label.fs);
    const detailSize = parseFloat(svc.detail.fs);
    check(
      nameSize > detailSize,
      `${at}  the service name is larger than the image line`,
      `${nameSize}px vs ${detailSize}px`,
    );
    check(
      Number(svc.label.fw) > Number(svc.detail.fw),
      `${at}  the service name is heavier than the image line`,
      `${svc.label.fw} vs ${svc.detail.fw}`,
    );
    check(
      ratio(svc.detail.fill, svc.fill) >= 3,
      `${at}  the image line still reads on the card`,
      `${svc.detail.fill} on ${svc.fill} = ${ratio(svc.detail.fill, svc.fill).toFixed(2)}:1`,
    );

    // 3b. EVERY EDGE KIND IS ACTUALLY DRAWN. A line at 65% opacity in a
    // half-transparent token is two multiplications away from the canvas, and
    // the relations are the reason this pane exists. Composited, both factors
    // included.
    for (const e of m.edges) {
      const r = ratio(e.stroke, m.canvas, Number(e.opacity));
      check(
        r !== null && r >= 2,
        `${at}  the ${e.kind} edge reads against the canvas`,
        `${e.stroke} at ${e.opacity} = ${r === null ? '?' : r.toFixed(2)}:1`,
      );
    }

    // 4. THE EDGE LABEL IS READABLE OVER ITS OWN PLATE — the plate exists
    // because the edge runs underneath the word.
    if (m.plate && m.edgeLabel) {
      check(
        ratio(m.edgeLabel.fill, m.plate.fill) >= 4.5,
        `${at}  an edge condition reads against its plate`,
        `${m.edgeLabel.fill} on ${m.plate.fill} = ${ratio(m.edgeLabel.fill, m.plate.fill).toFixed(2)}:1`,
      );
    }

    // 5. THE BADGE'S COUNT READS ON THE BADGE. It is a number on a filled dot
    // and it is the only thing on the canvas that says how many problems a
    // service has.
    if (m.badge && m.badgeCount) {
      check(
        ratio(m.badgeCount.fill, m.badge.fill) >= 3,
        `${at}  the finding count reads on its badge`,
        `${m.badgeCount.fill} on ${m.badge.fill} = ${ratio(m.badgeCount.fill, m.badge.fill).toFixed(2)}:1`,
      );
    }

    // 6. AUTO-ARRANGE. A real button, in the toolbar, with a name, reachable
    // by Tab — the same contract every other control in that row holds.
    const a = m.arrange;
    check(a !== null, `${at}  the toolbar carries an auto-arrange control`);
    if (a) {
      check(
        a.tag === 'BUTTON' && a.type === 'button' && (a.name || '').length > 0 && a.tabindex === null && a.disabled === false,
        `${at}  auto-arrange is a real, named, tab-reachable button`,
        `${a.tag} type=${a.type} name=${JSON.stringify(a.name)} tabindex=${a.tabindex} disabled=${a.disabled}`,
      );
    }
  }

  // 7. AUTO-ARRANGE, END TO END, with the browser's own mouse. Three rendered
  // facts: the drag moved the box, the press put it back, and what went to the
  // host was an EMPTY position map — the message `panelbehaviour.test.ts` pins
  // to the view-state path, never the file.
  const trip = await arrangeRoundTrip({ theme: 'dark', width: CHECK_WIDTH });
  check(
    trip.dragged !== trip.before,
    'stack/dark  a node can be dragged off its computed position',
    `${trip.before} -> ${trip.dragged}`,
  );
  check(
    trip.enabled === 'false',
    'stack/dark  auto-arrange becomes available once something has been dragged',
    `aria-disabled=${trip.enabled}`,
  );
  check(
    trip.after === trip.before,
    'stack/dark  pressing auto-arrange redraws the computed arrangement',
    `${trip.dragged} -> ${trip.after}, wanted ${trip.before}`,
  );
  const cleared = trip.posted.filter(
    (m) => m.type === 'positions' && Object.keys(m.positions).length === 0,
  );
  check(
    cleared.length === 1 && !trip.posted.some((m) => ['edit', 'stage', 'save'].includes(m.type)),
    'stack/dark  it clears the stored positions and writes nothing to the file',
    JSON.stringify(trip.posted.map((m) => m.type)),
  );
  check(/discarded/i.test(trip.status), 'stack/dark  it says what it discarded', trip.status);

  console.log(failed === 0 ? '\nthe canvas reads as the mockup does' : `\n${failed} check(s) failed`);
} else {
  const out = await measure({
    scenario: process.argv[2] || 'stack',
    theme: process.argv[3] || 'dark',
  });
  console.log(JSON.stringify(out, null, 1));
}

await browser.close();
server.close();
process.exit(failed === 0 ? 0 : 1);
