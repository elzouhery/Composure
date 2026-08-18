// The inspector's row rhythm and column geometry — instrument, and CHECK.
//
//   node harness/rhythm.mjs [scenario] [theme] [width] [height]   # dump
//   node harness/rhythm.mjs --check                               # exits 1
//
// It exists because `node --test` cannot resolve a `--vscode-*` token, lay out
// a grid or measure a pixel, and the two things the owner reacted to on
// 2026-08-13 are both geometry:
//
//   "the forms need some spacing"        — every row the same distance apart,
//                                          so a section heading, a key, a value
//                                          and a provenance line read at one
//                                          level;
//   "the lists look weird like this"     — and, underneath it, three different
//                                          value columns in one pane, because
//                                          each level of nesting pushed the
//                                          whole row right.
//
// The dump prints every element inside the pane in document order with its
// geometry. `--check` turns the two properties that must not regress into a
// non-zero exit: ONE value column at every depth on both surfaces, and a
// section gap strictly larger than a row gap.
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

/** Everything the page can tell us about one pane. */
async function measure({ scenario, theme = 'dark', width = 1100, height = 720 }) {
  const page = await browser.newPage();
  await page.setViewport({ width, height });
  await page.goto(`${base}?scenario=${scenario}&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });
  const out = await page.evaluate(() => {
    // Two panes carry `.inspector-body` — the inspector and the stage form —
    // and only one of them is on screen. Take the one with a box.
    const body = [...document.querySelectorAll('.inspector-body')].find(
      (e) => e.getBoundingClientRect().height > 0,
    );
    if (!body) return null;
    const rows = [];
    const walk = (node, depth) => {
      for (const el of node.children) {
        const r = el.getBoundingClientRect();
        const cs = getComputedStyle(el);
        rows.push({
          d: depth,
          cls: el.className,
          t: (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 54),
          x: +r.left.toFixed(1),
          y: +r.top.toFixed(1),
          w: +r.width.toFixed(1),
          h: +r.height.toFixed(1),
          fs: cs.fontSize,
          fw: cs.fontWeight,
          color: cs.color,
        });
        walk(el, depth + 1);
      }
    };
    walk(body, 0);
    // Where a value's INK starts: the chip's own left padding is part of the
    // column, so compare the text edge rather than the box edge.
    const inkLeft = (el) => {
      const r = el.getBoundingClientRect();
      return +(r.left + parseFloat(getComputedStyle(el).paddingLeft)).toFixed(1);
    };
    // The value COLUMN is the direct child of a row. A `.field-value` nested
    // inside a value cell — the paths after `COPY --from=build` — is a second
    // part of one value, not a second column.
    const cells = [...body.querySelectorAll('.field > .field-value')].filter(
      (e) => e.getBoundingClientRect().width > 0,
    );
    const subs = [...body.querySelectorAll('.prov, .resolution, .diag')].filter(
      (e) => e.getBoundingClientRect().width > 0,
    );
    const groups = [...body.querySelectorAll(':scope > .grp')].map((g) => {
      const r = g.getBoundingClientRect();
      return { name: (g.querySelector('.grp-name') || {}).textContent, top: +r.top.toFixed(1), bottom: +r.bottom.toFixed(1), h: Math.round(r.height) };
    });
    // Rows, tagged by WHICH section holds them — by element, not by class
    // name, or every section's first row is measured against the last row of
    // the section above and the gap reported is a section gap.
    // ADJACENT rows only. A section that interleaves rows with a nested group
    // has two rows 295px apart with a whole sub-form between them, and that is
    // not a row gap — measuring it as one reports a rhythm the pane has not got.
    const sections = [...body.querySelectorAll('.grp')];
    const rowGaps = [];
    for (const sec of sections) {
      const kids = [...sec.children];
      for (let i = 1; i < kids.length; i++) {
        const a = kids[i - 1];
        const b = kids[i];
        if (!a.classList.contains('field-block') || !b.classList.contains('field-block')) continue;
        rowGaps.push(
          +(b.getBoundingClientRect().top - a.getBoundingClientRect().bottom).toFixed(1),
        );
      }
    }
    // ---- the 2026-08-13 gap set, measured ---------------------------------
    //
    // Each of these is a rendered fact, never "a CSS rule exists": nineteen
    // checks that could not fail have shipped in this repository and two of
    // them shipped inside a story marked done.

    // A section's provenance must not sit between its heading and its first
    // row. `webstack/compose.yaml:36` there reads as `test`'s, because that is
    // what a provenance line means everywhere else on this pane.
    const provAboveFirstRow = [...body.querySelectorAll('.grp')]
      .filter((g) => g.children.length > 1 && g.children[1].classList.contains('prov'))
      .map((g) => (g.querySelector('.grp-name') || {}).textContent);

    // The same file position, linked twice under one row. Two buttons sixteen
    // pixels apart going to the same place is not two anchors.
    // Scoped to the block's OWN links. A nested entry row is a row in its own
    // right: `depends_on` and `depends_on.api` both linking `compose.yaml:42`
    // is the parent naming the mapping and the child naming the entry, which is
    // two rows saying where each of them is — not one row saying it twice.
    const repeatedAnchors = [];
    for (const blk of body.querySelectorAll('.field-block')) {
      const at = [...blk.querySelectorAll('.prov-link')]
        .filter((b) => b.closest('.field-block') === blk)
        .map((b) => b.dataset.at);
      const dupes = at.filter((v, i) => v !== undefined && at.indexOf(v) !== i);
      if (dupes.length > 0) {
        repeatedAnchors.push({ path: blk.dataset.path, at: [...new Set(dupes)] });
      }
    }

    // How tall the costliest single finding is, and how many elements it took.
    // A number rather than a verdict: the check is a ceiling, the dump is the
    // evidence for where the ceiling should be.
    // "Elements" counted the way a READER counts them — one per thing on screen
    // that carries a fact — not one per DOM node and not one per block-level
    // child. `SESSION_SECRET` cost six: the value chip, the severity pill, the
    // resolution sentence, the provenance line, the rule's message, and a
    // second provenance link to the position the first one already went to.
    // Scoped to the block's own carriers, so a mapping is not charged for the
    // rows underneath it.
    const CARRIERS = '.field-value, .pill, .resolution, .prov, .diag-text, .diag .prov-link';
    const findings = [...body.querySelectorAll('.field-block')]
      .filter((b) => b.querySelector(':scope > .diag'))
      .map((b) => ({
        path: b.dataset.path,
        h: Math.round(b.getBoundingClientRect().height),
        parts: [...b.querySelectorAll(CARRIERS)].filter(
          (e) => e.closest('.field-block') === b,
        ).length,
      }))
      .sort((a, b) => b.h - a.h);

    const size = (sel) => {
      const e = [...body.querySelectorAll(sel)].find((x) => x.getBoundingClientRect().width > 0);
      return e ? parseFloat(getComputedStyle(e).fontSize) : null;
    };

    // The pane header this view is showing, and where its controls sit.
    const head = body.parentElement.querySelector('.pane-header');
    const hb = head ? head.getBoundingClientRect() : null;
    const ctl = head ? head.querySelector('.pane-control') : null;
    const sum = head ? head.querySelector('.pane-summary') : null;
    const header = head
      ? {
          h: Math.round(hb.height),
          summary: sum ? (sum.textContent || '').trim() : null,
          summaryX: sum ? Math.round(sum.getBoundingClientRect().left) : null,
          control: ctl ? (ctl.textContent || '').trim() : null,
          // Same line as the summary, and to the RIGHT of it: a control that
          // wrapped to a row of its own is the defect, and both halves of that
          // — the row and the left edge — have to be measured.
          //
          // "Same line" is vertical OVERLAP, not an equal top. The two are
          // baseline-aligned and different heights, so a 17px button next to a
          // 13px span sits two pixels higher while being unambiguously on the
          // same line; an equal-top check would fail on a correct header.
          controlX: ctl ? Math.round(ctl.getBoundingClientRect().left) : null,
          controlTop: ctl ? Math.round(ctl.getBoundingClientRect().top) : null,
          controlBottom: ctl ? Math.round(ctl.getBoundingClientRect().bottom) : null,
          summaryTop: sum ? Math.round(sum.getBoundingClientRect().top) : null,
          summaryBottom: sum ? Math.round(sum.getBoundingClientRect().bottom) : null,
        }
      : null;

    // The graph's raised surfaces, resolved by the engine rather than compared
    // as tokens — a fill and the canvas behind it can be two different tokens
    // that name the same colour, which is the whole `.node-port` residue.
    const paint = (sel, prop) => {
      const e = document.querySelector(sel);
      return e ? getComputedStyle(e)[prop] : null;
    };
    const surfaces = {
      canvas: paint('.canvas', 'backgroundColor'),
      portFill: paint('.node-port .node-box', 'fill'),
      portLabel: paint('.node-port .node-label', 'fill'),
      nodeFill: paint('.node-box', 'fill'),
    };

    return {
      paneW: Math.round(body.getBoundingClientRect().width),
      bodyH: Math.round(body.getBoundingClientRect().height),
      scrollH: body.scrollHeight,
      provAboveFirstRow,
      repeatedAnchors,
      findings,
      header,
      surfaces,
      type: {
        value: size('.field-value'),
        addableKey: size('.addable-key'),
        addableLead: size('.addable-lead'),
      },
      valueInk: cells.map(inkLeft),
      valueBox: cells.map((e) => +e.getBoundingClientRect().left.toFixed(1)),
      subInk: subs.map(inkLeft),
      valueWords: cells.map((e) => (e.textContent || e.value || '').trim()),
      groups,
      rowGaps,
      counts: {
        field: body.querySelectorAll('.field').length,
        prov: body.querySelectorAll('.prov').length,
        addableKey: body.querySelectorAll('.addable-key').length,
      },
      rows,
    };
  });
  await page.close();
  return out;
}

const uniq = (xs) => [...new Set(xs)];
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
  for (const scenario of ['service', 'dockerfile']) {
    for (const theme of ['dark', 'light']) {
      const m = await measure({ scenario, theme, width: CHECK_WIDTH });
      const label = `${scenario}/${theme}`;
      // ONE value column. Every value in the pane starts its ink on the same
      // x, whatever depth it is at — the nesting step comes out of the key
      // column rather than moving the row.
      const cols = uniq(m.valueInk.map((x) => Math.round(x)));
      check(cols.length === 1, `${label}  one value column`, `x = ${cols.join(', ')}`);
      // A provenance line, a resolution sentence and a diagnostic all belong to
      // the value above them, so they begin where it begins.
      // …and they align on the chip's BOX, which is where the mockup puts them
      // (`.prov { padding-left: 104px }` against `.k` 96 + an 8px gap), so the
      // subordinate line sits a chip's padding inside its value.
      const boxes = uniq(m.valueBox.map((x) => Math.round(x)));
      const subs = uniq(m.subInk.map((x) => Math.round(x)));
      check(
        subs.every((x) => boxes.includes(x)),
        `${label}  provenance lines start at the value column`,
        `x = ${subs.join(', ')} against ${boxes.join(', ')}`,
      );
      // Our words for a value's shape are not values.
      check(
        !m.valueWords.some((w) => w === 'list' || w === 'map'),
        `${label}  no cell reads "list" or "map"`,
      );

      // GAP 6. A nested mapping's provenance belonged to the heading, and sat
      // above the first row instead — where every other provenance line on this
      // pane means "the value ABOVE came from here", so it claimed to be the
      // first row's. `HEALTHCHECK` / `webstack/compose.yaml:36` / `test`.
      check(
        m.provAboveFirstRow.length === 0,
        `${label}  no provenance line between a heading and its first row`,
        m.provAboveFirstRow.length > 0 ? `in ${m.provAboveFirstRow.join(', ')}` : '',
      );

      // GAP 1, first half. A finding anchored where the value already is used
      // to print that position a second time, as a second button under the same
      // row. Skipping a POSITION is not skipping a finding: a finding anchored
      // somewhere else still prints every anchor it has.
      check(
        m.repeatedAnchors.length === 0,
        `${label}  no file position is linked twice under one row`,
        m.repeatedAnchors.map((r) => `${r.path} → ${r.at.join(' ')}`).join('; '),
      );

      // GAP 1, second half. The ceiling on what one finding costs. Six stacked
      // elements and 117px measured on `SESSION_SECRET` before this; the
      // mockup carries a finding as an inline pill plus a line. The floor under
      // this number is fixed by what may NOT be dropped — the pill, the
      // provenance line and the rule's own message all stay — so it can only be
      // met by not saying the same thing twice.
      const worst = m.findings[0];
      if (worst) {
        check(
          worst.parts <= 4 && worst.h <= 90,
          `${label}  a finding costs at most 4 elements and 90px`,
          `worst ${worst.path} ${worst.parts} elements, ${worst.h}px`,
        );
      }

      // GAP 2. The `available, not set` keys read as a wall of links at the
      // pane's own code size. They are an INDEX of what could be set, not
      // values, and the mockup runs them a step below everything else. Every
      // key is still rendered and still a button — only the size moved, which
      // is why this compares sizes and not counts.
      check(
        m.type.addableKey !== null && m.type.value !== null && m.type.addableKey < m.type.value,
        `${label}  an available key is quieter than a value`,
        `key ${m.type.addableKey}px vs value ${m.type.value}px`,
      );
    }
  }

  // GAPS 3 and 4 — the Dockerfile pane header.
  //
  // The summary is what the reader looks at first and the header had none. The
  // control belongs at the right of that same line; it shipped as a filled
  // primary button on a row of its own beneath it.
  //
  // Base-image age is deliberately NOT asserted and deliberately not rendered:
  // it needs a registry round-trip that is deferred to phase 6, and a header
  // reading "14 months old" because the mockup does would be invented.
  for (const theme of ['dark', 'light']) {
    const m = await measure({ scenario: 'dockerfile', theme, width: CHECK_WIDTH });
    const h = m.header ?? {};
    check(
      /^\d+ stages? · \d+ instructions? adds? a layer$/.test(h.summary ?? ''),
      `dockerfile/${theme}  the header says what is in the file`,
      JSON.stringify(h.summary),
    );
    const sameLine =
      h.controlTop < h.summaryBottom && h.summaryTop < h.controlBottom;
    check(
      h.control === '+ add stage' && sameLine && h.controlX > h.summaryX,
      `dockerfile/${theme}  the header control is on the header line, at its right`,
      `control ${JSON.stringify(h.control)} x=${h.controlX} y=${h.controlTop}..${h.controlBottom}` +
        ` vs summary x=${h.summaryX} y=${h.summaryTop}..${h.summaryBottom}`,
    );
  }
  // GAP 5 — the port node box, the last surface `RAISED_SURFACES` recorded as
  // live residue.
  //
  // It was drawn on `input-background` because a published port names something
  // outside the stack. In Light+ that token and `editor-background` resolve to
  // the same colour, so the box was white on white behind a hairline: ratio
  // 1.000:1, the exact failure the value chip was fixed for. Resolved by the
  // engine, because two different tokens naming one colour is precisely what a
  // token comparison cannot see.
  for (const theme of ['dark', 'light']) {
    const s = (await measure({ scenario: 'service', theme, width: CHECK_WIDTH })).surfaces;
    const rgb = (v) => (v ?? '').match(/[\d.]+/g)?.map(Number) ?? null;
    const lin = (c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
    const lum = ([r, g, b]) => 0.2126 * lin(r / 255) + 0.7152 * lin(g / 255) + 0.0722 * lin(b / 255);
    const port = rgb(s.portFill);
    const canvas = rgb(s.canvas);
    const ratio =
      port && canvas
        ? (() => {
            const [x, y] = [lum(port), lum(canvas)].sort((p, q) => q - p);
            return (x + 0.05) / (y + 0.05);
          })()
        : null;
    check(
      ratio !== null && ratio > 1.02,
      `service/${theme}  the port node box is not the canvas colour`,
      `${s.portFill} on ${s.canvas} = ${ratio === null ? 'unresolvable' : `${ratio.toFixed(3)}:1`}`,
    );
    // The label moved with the box and had to: `input-foreground` is guaranteed
    // against `input-background` and against nothing else, so a box that leaves
    // that surface drags its text off its only guaranteed pairing. Measured as
    // "the port label is painted like every other node label", which is the
    // thing that stops the two drifting apart again.
    const s2 = (await measure({ scenario: 'service', theme, width: CHECK_WIDTH })).surfaces;
    check(
      s2.portFill === s2.nodeFill,
      `service/${theme}  the port box takes the same surface as every node box`,
      `${s2.portFill} vs ${s2.nodeFill}`,
    );
  }

  // Rhythm: a section is further from the section above it than a row is from
  // the row above it. Equal gaps are the defect — everything reads at one level.
  const m = await measure({ scenario: 'service', theme: 'dark', width: CHECK_WIDTH });
  const gaps = (xs) => xs.slice(1).map((b, i) => Math.round(b.top - xs[i].bottom));
  const sectionGaps = gaps(m.groups);
  const rowGaps = m.rowGaps.map((g) => Math.round(g));
  const minSection = Math.min(...sectionGaps);
  const maxRow = Math.max(...rowGaps);
  check(
    minSection > maxRow && minSection >= 16,
    'service/dark  a section is further apart than a row',
    `section ${sectionGaps.join(',')}px vs row ${rowGaps.join(',')}px`,
  );
  // The other half of "everything reads at one level": a section heading and a
  // provenance line were the same size, the same colour AND the same weight,
  // so nothing but a hairline rule told them apart. The type size is the
  // reader's, and the colour is the token's; weight is the axis left.
  const weightOf = (cls) => (m.rows.find((r) => r.cls === cls) || {}).fw;
  const heading = weightOf('grp-name');
  const prov = weightOf('prov');
  check(
    heading !== undefined && prov !== undefined && heading !== prov,
    'service/dark  a section heading is not the same weight as a provenance line',
    `grp-name ${heading} vs prov ${prov}`,
  );
  console.log(
    failed === 0 ? '\nthe pane has one value column and a rhythm' : `\n${failed} check(s) failed`,
  );
} else {
  const out = await measure({
    scenario: process.argv[2] || 'service',
    theme: process.argv[3] || 'dark',
    width: Number(process.argv[4] || 1100),
    height: Number(process.argv[5] || 720),
  });
  console.log(JSON.stringify(out, null, 1));
}

await browser.close();
server.close();
process.exit(failed === 0 ? 0 : 1);
