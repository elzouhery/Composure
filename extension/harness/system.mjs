// Is the pane ONE design? — instrument, and CHECK.
//
//   node harness/system.mjs [scenario] [theme]   # dump the vocabularies
//   node harness/system.mjs --check              # exits 1
//
// `rhythm.mjs` measures the inspector's geometry, `canvas.mjs` the graph's
// appearance and `epic9.mjs` what three gestures produce. None of the three can
// see the thing the owner has now said three times: that the product does not
// look like the design. That is not one geometry or one colour — it is whether
// the parts added since the mockup belong to the same system as the parts drawn
// from it.
//
// Since the mockup was agreed, the pane gained eight things it has no
// counterpart for: the allowed-value combobox, the Docker Hub upgrade pill, the
// image search, the comment affordance, the move-to-variable block, list-entry
// expansion, the availability note and auto-arrange. Each was built by a
// different pass. Measured on the bundle before this one:
//
//   * TEN distinct font sizes were painted on two panes — 8.64, 9, 9.6, 10.2,
//     11.05, 11.52, 11.96, 12, 12.48 and 13px — from seven different
//     multipliers and one literal. `#` and `${}` sit in the same cell of the
//     same row at 12.48 and 11.52;
//   * NINE distinct button shapes, four of them for controls that act on a row:
//     17px, 16px, 13px and 13px tall, two faces, two radii;
//   * the row-action glyphs landed on EIGHT x positions — 724, 761, 762, 921,
//     1042, 1063, 1072, 1076 — because a parent row auto-placed them into the
//     value track, 350px left of every other one;
//   * two chrome controls whose own stylesheet comment says they share a
//     treatment rendered 22px and 17px tall.
//
// None of that is a bug and no test could fail on it. It is what "a mockup with
// eight additions bolted on" measures as, and this file is the ledger that
// keeps it closed. Every assertion is a RENDERED fact — a resolved size, a
// painted box — never "a CSS rule exists".
//
// Each check was falsified before it was trusted; the recipes are in the
// comment above each one.
import http from 'node:http';
import { mkdir, readFile } from 'node:fs/promises';
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

/**
 * The host's answer to a move, as a fixed value — the same shape and the same
 * reason as `epic9.mjs`'s: a picture whose contents depend on what a filesystem
 * happened to hold is a different picture every time it is taken.
 */
const EXTRACT_ANSWER = {
  name: 'WEB_IMAGE',
  value: 'ghcr.io/shipyard/web:2.4.1',
  compose: {
    file: 'webstack/compose.yaml',
    ops: [],
    diff:
      '--- a/compose.yaml\n+++ b/compose.yaml\n@@ -8,7 +8,7 @@\n   web:\n' +
      '-    image: ghcr.io/shipyard/web:2.4.1\n+    image: ${WEB_IMAGE}\n',
    added: 1,
    removed: 1,
    changed_lines: 2,
    written: false,
  },
  env_file: '/w/.env',
  env_diff: '--- a/.env\n+++ b/.env\n@@ -0,0 +1 @@\n+WEB_IMAGE=ghcr.io/shipyard/web:2.4.1\n',
  env_line: 'WEB_IMAGE=ghcr.io/shipyard/web:2.4.1',
  env_created: true,
  env_unchanged: false,
  written: false,
};

/**
 * Opens the three Epic 9 affordances, so the checks below measure the pane as
 * the reader has it rather than as it arrives.
 *
 * This is the whole reason this file duplicates a gesture `epic9.mjs` already
 * drives: the additions that broke the system are behind a press, so a scan of
 * the initial render is blind to exactly the controls in question.
 */
async function openEverything(page) {
  await page.evaluate(async (ex) => {
    const settle = () => new Promise((r) => setTimeout(r, 90));
    const body = [...document.querySelectorAll('.inspector-body')].find(
      (e) => e.getBoundingClientRect().height > 0,
    );
    body.querySelector('.extract-open[data-path="services.web.image"]').click();
    await settle();
    window.postMessage(
      { type: 'extract', file: 'x', path: 'services.web.image', staged: false, result: ex },
      '*',
    );
    await settle();
    body.querySelectorAll('[data-path="services.web.healthcheck.test"] .field-item')[1].click();
    await settle();
    body.querySelector('.comment-open[data-path="services.web.restart"]').click();
    await settle();
    window.postMessage(
      { type: 'comments', file: 'x', path: 'services.web.restart', staged: [], above: 'a\nb', trailing: 'c' },
      '*',
    );
    await settle();
  }, EXTRACT_ANSWER);
  await new Promise((r) => setTimeout(r, 200));
}

/** Everything this file needs to know about one rendered pane. */
async function measure({ scenario, theme = 'dark', width = 1100, height = 780, open = false }) {
  const page = await browser.newPage();
  await page.setViewport({ width, height });
  await page.goto(`${base}?scenario=${scenario}&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });
  if (open) {
    await openEverything(page);
  }
  const out = await page.evaluate(() => {
    const vis = (e) => {
      const r = e.getBoundingClientRect();
      return r.width > 0 && r.height > 0;
    };
    const mono = (f) => /mono|menlo|consolas|courier/i.test(f);
    const px = (v) => Math.round(parseFloat(v) * 100) / 100;

    // The declared scale, RESOLVED by the engine. Reading the custom property
    // back gives `calc(12px * 0.85)`; painting it gives 10.2px, and 10.2px is
    // what the reader sees.
    const probe = document.createElement('span');
    probe.textContent = 'x';
    probe.style.position = 'absolute';
    probe.style.visibility = 'hidden';
    document.body.appendChild(probe);
    const scale = {};
    for (const step of ['ui', 'code', 'label', 'sub', 'index', 'micro']) {
      probe.style.fontSize = `var(--type-${step})`;
      scale[step] = px(getComputedStyle(probe).fontSize);
    }
    probe.remove();

    // Every size actually painted, with an example of each. Leaves only: an
    // element whose text all lives in its children is not painting that text.
    const sizes = new Map();
    for (const e of [...document.querySelectorAll('#app *')].filter(vis)) {
      const text = (e.textContent || '').replace(/\s+/g, ' ').trim();
      if (!text || [...e.children].some((c) => (c.textContent || '').trim())) {
        continue;
      }
      const s = px(getComputedStyle(e).fontSize);
      const v = sizes.get(s) || { n: 0, eg: [] };
      v.n++;
      if (v.eg.length < 3) v.eg.push(`${String(e.className).slice(0, 26)}:${text.slice(0, 18)}`);
      sizes.set(s, v);
    }

    // Every corner painted, HTML and SVG kept apart. They answer different
    // questions: a chrome corner is one number (DESIGN.md `rounded.field` 3px)
    // and a node's corner is what tells the reader WHICH KIND of thing it is —
    // a service card, a resource capsule, a published port — so the graph has a
    // vocabulary of shapes and the chrome has one shape.
    const radii = new Map();
    const nodeRadii = new Map();
    for (const e of [...document.querySelectorAll('#app *')].filter(vis)) {
      const cs = getComputedStyle(e);
      const svg = e.tagName === 'rect';
      // A NODE box carries the kind vocabulary. Every other painted rect — the
      // plate behind an edge condition, for instance — is chrome and is held to
      // the chrome's one corner.
      const isNode = svg && String(e.className.baseVal ?? '').includes('node-box');
      const r = svg ? cs.rx : cs.borderRadius;
      if (!r || r === 'auto') continue;
      if (!isNode && r === '0px') continue;
      const into = isNode ? nodeRadii : radii;
      const v = into.get(r) || { n: 0, eg: [] };
      v.n++;
      if (v.eg.length < 3) {
        v.eg.push(String(e.className.baseVal ?? e.className ?? '').slice(0, 30));
      }
      into.set(r, v);
    }

    /** The shape of a control, as the browser painted it. */
    const shapeOf = (e) => {
      const cs = getComputedStyle(e);
      return {
        h: Math.round(e.getBoundingClientRect().height),
        pad: cs.padding,
        radius: cs.borderRadius,
        border: `${cs.borderWidth} ${cs.borderStyle}`,
        size: px(cs.fontSize),
      };
    };

    // Row actions: every button inside a `.field-actions` cell.
    const rowActions = [...document.querySelectorAll('.field-actions button')]
      .filter(vis)
      .map((e) => ({
        cls: String(e.className),
        t: (e.textContent || '').trim().slice(0, 10),
        named: e.classList.contains('row-action'),
        face: mono(getComputedStyle(e).fontFamily) ? 'mono' : 'ui',
        ...shapeOf(e),
      }));

    // The gutter: where each row's rightmost IN-FLOW child ends, and where its
    // actions cell begins. Absolutely positioned children are out of the row's
    // flow and are not part of its right edge.
    const rows = [...document.querySelectorAll('.field')].filter(vis).map((f) => {
      const kids = [...f.children].filter(
        (c) => vis(c) && getComputedStyle(c).position !== 'absolute',
      );
      const acts = f.querySelector(':scope > .field-actions');
      return {
        path: f.parentElement?.dataset?.path ?? '',
        right: Math.round(Math.max(...kids.map((c) => c.getBoundingClientRect().right))),
        actionsLeft: acts && vis(acts) ? Math.round(acts.getBoundingClientRect().left) : null,
        valueLeft: (() => {
          const v = f.querySelector(':scope > .field-value');
          return v && vis(v) ? Math.round(v.getBoundingClientRect().left) : null;
        })(),
      };
    });

    // The two chrome controls whose stylesheet says they share a treatment.
    const chrome = ['.toolbar-button', '.button-quiet'].map((sel) => {
      const e = [...document.querySelectorAll(sel)].find(vis);
      return { sel, shape: e ? shapeOf(e) : null };
    });

    // The Dockerfile pane's instruction labels, and whether they are in the
    // face DESIGN.md reserves for bytes that are in the file.
    const keys = [...document.querySelectorAll('.field > .field-key')].filter(vis).map((e) => ({
      t: (e.textContent || '').trim(),
      face: mono(getComputedStyle(e).fontFamily) ? 'mono' : 'ui',
      size: px(getComputedStyle(e).fontSize),
    }));

    /**
     * The add-a-key composer, and the mappings it is and is NOT offered on.
     *
     * A free-form mapping (`environment`, `labels`, `build.args`) permits any
     * key, so `stack/schema` has no `available, not set` list for it — and that
     * list was this pane's only route to a key the file does not have. The
     * composer is that route. What has to stay true is not that it EXISTS but
     * that it is offered on exactly the mappings that need it: a composer on
     * `healthcheck`, whose keys the specification names and which carries its
     * own available list, is the pane inventing a Compose key list, which is
     * the thing AD-20 forbids.
     *
     * The two kinds of mapping are told apart by how the pane DREW them, which
     * is the only distinction visible from here and is the right one: a
     * described mapping becomes a `.grp[data-path]` sub-group with an available
     * list under it, and a free-form one stays a row with a `.value-tree` of
     * its own keys, because the core sends it no children to make a group from.
     */
    const addKey = (() => {
      const blocks = [...document.querySelectorAll('.is-add-key')].filter(vis);
      const inputsOf = (b) =>
        [...b.querySelectorAll('input')].map((i) => {
          const cs = getComputedStyle(i);
          return {
            cls: String(i.className),
            named: (i.getAttribute('aria-label') || '').trim().length > 0,
            placeholder: i.placeholder,
            left: Math.round(i.getBoundingClientRect().left),
            face: mono(cs.fontFamily) ? 'mono' : 'ui',
            size: px(cs.fontSize),
          };
        });
      return {
        on: blocks.map((b) => b.dataset.addKey).sort(),
        // Mappings drawn as a row with their own keys under it.
        treeMappings: [...document.querySelectorAll('.field-block[data-path]')]
          .filter((b) => vis(b) && b.querySelector(':scope > .value-tree'))
          .map((b) => b.dataset.path)
          .sort(),
        // Mappings drawn as a sub-group, which is what the core sending
        // children means and what a DESCRIBED mapping gets.
        describedMappings: [...document.querySelectorAll('.grp[data-path]')]
          .filter(vis)
          .map((g) => g.dataset.path)
          .sort(),
        onDescribed: [...document.querySelectorAll('.grp[data-path] .is-add-key')]
          .filter(vis)
          .map((b) => b.dataset.addKey),
        fields: blocks.flatMap(inputsOf),
        // A composer is two FIELDS. Any button in it would be a sixth control
        // shape in a pane that has just been reduced to one.
        buttons: blocks.reduce((n, b) => n + b.querySelectorAll('button').length, 0),
        // Where the keys of the mapping it belongs to start, so "the key field
        // is in the key column" is measured rather than asserted.
        siblingKeys: blocks.map((b) => {
          const owner = document.querySelector(
            `.field-block[data-path="${b.dataset.addKey}"]`,
          );
          const keys = owner
            ? [...owner.querySelectorAll(':scope > .value-tree .field > .field-key')].filter(vis)
            : [];
          return {
            path: b.dataset.addKey,
            column: keys.length > 0 ? Math.round(Math.min(...keys.map((e) => e.getBoundingClientRect().left))) : null,
            // The composer's FIRST field, found by position rather than by
            // class: a check that queries `input.field-key` measures nothing at
            // all when the class is the thing that was dropped, and reports
            // `null` instead of the 800 that says the field landed in the value
            // track. This measures where the reader's caret actually goes.
            composer: (() => {
              const k = b.querySelector('input');
              return k && vis(k) ? Math.round(k.getBoundingClientRect().left) : null;
            })(),
          };
        }),
      };
    })();

    return {
      scale,
      addKey,
      sizes: [...sizes.entries()].sort((a, b) => a[0] - b[0]).map(([s, v]) => ({ s, ...v })),
      radii: [...radii.entries()].map(([r, v]) => ({ r, ...v })),
      nodeRadii: [...nodeRadii.entries()].map(([r, v]) => ({ r, ...v })),
      rowActions,
      rows,
      chrome,
      keys,
      // How much of the graph pane is chrome before a node is drawn. The
      // mockup has one 5px-padded header and nothing else (directions-3.html:
      // 277); the gap report measured 94px of a 693px pane, 14%. This is the
      // number that grows silently — four pixels of button padding wrapped the
      // toolbar and put 28px back on it while every other check stayed green.
      chrome_px: (() => {
        const pane = document.querySelector('.pane-graph');
        const canvas = document.querySelector('.canvas');
        if (!pane || !canvas || !vis(canvas)) return null;
        return Math.round(
          canvas.getBoundingClientRect().top - pane.getBoundingClientRect().top,
        );
      })(),
      // Did the toolbar WRAP? Not "how many distinct tops" — the controls are
      // baseline-aligned and a 13px label sits five pixels below a 22px button
      // on the same line, so counting tops reports two rows for a row that has
      // not wrapped. The wrap is a height: one row is the tallest control plus
      // the bar's own padding, and a second row is that again.
      toolbarWrapped: (() => {
        const bar = document.querySelector('.toolbar');
        if (!bar || !vis(bar)) return null;
        const cs = getComputedStyle(bar);
        const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
        const tallest = Math.max(
          ...[...bar.children].filter(vis).map((e) => e.getBoundingClientRect().height),
        );
        return bar.getBoundingClientRect().height > tallest + pad + 1;
      })(),
      // How many horizontal rules stand between the graph pane's header and its
      // canvas. The mockup has one 5px-padded header and nothing else.
      chromeRules: (() => {
        const pane = document.querySelector('.pane-graph');
        if (!pane) return null;
        return [...pane.children].filter(
          (e) =>
            vis(e) &&
            !e.classList.contains('canvas') &&
            !e.classList.contains('pane-header') &&
            getComputedStyle(e).borderBottomWidth !== '0px',
        ).length;
      })(),
    };
  });
  await page.close();
  return out;
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
const uniq = (xs) => [...new Set(xs)];

if (process.argv.includes('--shots')) {
  // The add-a-key composer, photographed in both default themes.
  //
  // A capture rather than only a check because the thing the owner reported is
  // an APPEARANCE — three keys and nothing that adds — and a number saying a
  // composer exists is not a picture of one. The shot is the inspector's own
  // rectangle scrolled to the free-form mapping, for the same reason
  // `epic9.mjs --shots` frames each affordance: a full-panel capture puts the
  // row below the fold and photographs the canvas instead.
  const dir =
    process.argv[process.argv.indexOf('--shots') + 1] ||
    process.env.SHOTS ||
    path.join(ext, '..', 'shots-freeform');
  await mkdir(dir, { recursive: true });
  for (const theme of ['dark', 'light']) {
    const page = await browser.newPage();
    await page.setViewport({ width: 1100, height: 780 });
    await page.goto(`${base}?scenario=service&theme=${theme}`, { waitUntil: 'networkidle0' });
    await page.waitForFunction('window.__ready === true', { timeout: 15000 });
    const found = await page.evaluate(() => {
      const composer = document.querySelector('.is-add-key');
      if (!composer) return null;
      composer.scrollIntoView({ block: 'center' });
      return composer.dataset.addKey;
    });
    if (found === null) {
      console.error('no add-a-key composer on the service pane — nothing to photograph');
      failed++;
    }
    await new Promise((r) => setTimeout(r, 120));
    const pane = await page.$('.inspector');
    const shot = path.join(dir, `freeform-add-key-${theme === 'dark' ? 'dark-plus' : 'light-plus'}.png`);
    await pane.screenshot({ path: shot });
    console.log(`wrote ${shot}  (${found})`);
    await page.close();
  }
} else if (process.argv.includes('--check')) {
  const cases = [
    { scenario: 'service', theme: 'dark' },
    { scenario: 'service', theme: 'light' },
    { scenario: 'service', theme: 'dark', open: true },
    { scenario: 'service', theme: 'light', open: true },
    { scenario: 'dockerfile', theme: 'dark' },
    { scenario: 'dockerfile', theme: 'light' },
    { scenario: 'stack', theme: 'dark' },
    { scenario: 'stack', theme: 'light' },
  ];

  for (const c of cases) {
    const m = await measure(c);
    const label = `${c.scenario}/${c.theme}${c.open ? '/opened' : ''}`;
    const steps = uniq(Object.values(m.scale));

    // ---- 1. The type scale is CLOSED. -----------------------------------
    //
    // Every size painted is one of the declared steps. Not "how many sizes" —
    // a count can be met by picking any five — but "which", so a rule that
    // invents `11px` fails whatever else is on the pane.
    //
    // Falsified by adding `font-size: 11px` to any rule in style.css: this
    // reports `stray 11px` and exits 1. Confirmed before this was trusted.
    const stray = m.sizes.filter((s) => !steps.includes(s.s));
    check(
      stray.length === 0,
      `${label}  every painted size is a step of the declared scale`,
      stray.length > 0
        ? `stray ${stray.map((s) => `${s.s}px (${s.eg[0]})`).join(', ')}`
        : `${m.sizes.length} size(s): ${m.sizes.map((s) => s.s).join(', ')} from ${steps.join(', ')}`,
    );

    // ---- 2. A control that acts on a row has ONE shape. ------------------
    //
    // Box, type step, radius and hit area — everything except the FACE, which
    // is DESIGN.md's monospace rule and is allowed to differ between `#` (a
    // mark the file uses) and `Remove` (our word).
    //
    // Falsified by restoring `.comment-open { font-size: 0.78rem }`: this
    // reports two shapes and exits 1.
    if (m.rowActions.length > 0) {
      const shapes = uniq(
        m.rowActions.map((a) => `${a.h}px ${a.pad} ${a.radius} ${a.border} ${a.size}px`),
      );
      check(
        shapes.length === 1,
        `${label}  every row-action control has one shape`,
        `${m.rowActions.length} control(s), ${shapes.length} shape(s): ${shapes.join(' | ')}`,
      );
      // …and each is NAMED as one, so the rule above is the rule it took its
      // box from rather than a coincidence of four separate declarations.
      const unnamed = m.rowActions.filter((a) => !a.named);
      check(
        unnamed.length === 0,
        `${label}  every row-action control is declared one`,
        unnamed.map((a) => `${a.cls}:${a.t}`).join(', '),
      );
    }

    // ---- 3. The row's right edge is a GUTTER. ---------------------------
    //
    // The counterpart of `rhythm.mjs`'s one value column, for the other side of
    // the row. Every row ends on the same x whatever it happens to carry, and
    // no row's actions sit left of its own value — which is what a parent row
    // did, putting `#` hard against the word `environment`.
    //
    // Falsified by removing `.field > .field-actions { grid-column: 4 }`: the
    // parent rows report `actions left of the value` and the right edges split
    // into two. Confirmed both ways.
    if (m.rows.length > 0) {
      const edges = uniq(m.rows.map((r) => r.right));
      check(
        edges.length === 1,
        `${label}  every field row ends on the same x`,
        `x = ${edges.sort((a, b) => a - b).join(', ')} over ${m.rows.length} rows`,
      );
      const column = Math.min(...m.rows.map((r) => r.valueLeft ?? Infinity));
      const early = m.rows.filter((r) => r.actionsLeft !== null && r.actionsLeft < column);
      check(
        early.length === 0,
        `${label}  no row puts its controls left of the value column`,
        early.map((r) => `${r.path} at ${r.actionsLeft} < ${column}`).join('; '),
      );
    }

    // ---- 4. One chrome corner, and a graph vocabulary that is closed. ----
    //
    // DESIGN.md `rounded.field` is 3px, and every box in the chrome is on it.
    // Two were not: the search field at 2px and `Stage this move` at 2px, each
    // sitting beside five 3px corners and belonging to nothing.
    //
    // The graph is the other case, and it is NOT a violation to be tidied away.
    // A node's corner is how the reader tells what kind of thing it is —
    // DESIGN.md gives the service card 4px, and story 4.2's resource capsule
    // (12px) and published-port square (0) are the same idea carried to node
    // kinds the mockup never drew. So the rule there is that the vocabulary is
    // CLOSED — three shapes, each meaning one kind — rather than that it is
    // one number. A fourth corner appearing in the graph fails this.
    const strayR = m.radii.filter((r) => r.r !== '3px');
    check(
      strayR.length === 0,
      `${label}  every corner in the chrome is 3px`,
      strayR.map((r) => `${r.r} (${r.eg[0]})`).join(', '),
    );
    // ---- 3b. The add-a-key composer is offered where it is needed. -------
    //
    // The gap it closes: a free-form mapping has no `available, not set` list,
    // because the specification names none of its keys — and that list was the
    // only route this pane offered to a key the file does not have. A reader
    // looking at `environment` with three keys had nowhere to add a fourth.
    //
    // The property is not "a composer exists". It is that the composer is
    // offered on the mappings that have no other route and on NO OTHERS, and
    // that it took no new shape to do it.
    //
    // WHICH mappings, named — not how many. A count is met by any set of the
    // right size, and the sets below are the real core's answer about
    // `examples/webstack`: `environment` and `depends_on` on the service pane
    // (both free-form), and on the stack pane the reader's own `x-` block and
    // NOT `services` / `networks` / `volumes`, which `+ add` declares properly
    // and which a raw key composer would let the reader write `foo:` into.
    //
    // Falsified before it was trusted:
    //   * deleting `!ADD_BLOCKS.has(field.path)` from `canAddKey`: the stack
    //     pane reports `networks, services, volumes, x-service-defaults`
    //     against `x-service-defaults`, exit 1;
    //   * deleting the `addKeyRow` call: every scenario reports `nothing`;
    //   * placing the key field with anything but `.field-key`: it auto-places
    //     into the value track and reports `key field at 800, column 688`.
    //
    // What this canNOT falsify, stated so nobody trusts it to: dropping the
    // `free_form` test from `canAddKey`. On real core answers a mapping the
    // specification DESCRIBES arrives with children and is drawn as a
    // `.grp` sub-group, which `field()` returns before it ever reaches the
    // composer — so on this data the two rules agree. The shape that
    // distinguishes them is a described mapping the core sent no children for,
    // which `webview/testdom.test.ts` renders directly, and whether the core
    // marks the two kinds apart at all is `host/realcore.test.ts` against the
    // shipped binary. Three checks, because no one of them sees the whole
    // property.
    const EXPECTED = {
      service: ['services.web.depends_on', 'services.web.environment'],
      stack: ['x-service-defaults'],
      dockerfile: [],
    };
    const k = m.addKey;
    {
      const want = EXPECTED[c.scenario];
      check(
        JSON.stringify(k.on) === JSON.stringify(want),
        `${label}  a mapping whose keys the schema does not name can be added to`,
        `composer on ${k.on.join(', ') || 'nothing'}; expected ${want.join(', ') || 'nothing'} ` +
          `of ${k.treeMappings.length} mapping(s) drawn`,
      );
      // The negative that IS visible here: a mapping the specification
      // describes is drawn as a sub-group with its own available-key list, and
      // no composer may appear inside one.
      check(
        k.onDescribed.length === 0,
        `${label}  no composer on a mapping the specification describes`,
        `described: ${k.describedMappings.join(', ') || 'none'}; ` +
          `offered on: ${k.onDescribed.join(', ')}`,
      );
    }
    if (k.on.length > 0) {
      // Two fields, both named, both in the code face — what goes in them is
      // bytes that end up in the file — and no button, so the row-action
      // vocabulary this file just reduced to one shape stays at one shape.
      check(
        k.fields.length === k.on.length * 2 &&
          k.fields.every((f) => f.named && f.face === 'mono') &&
          k.buttons === 0,
        `${label}  the composer is two named fields in the code face and no new control`,
        `${k.fields.length} field(s) for ${k.on.length} composer(s), ${k.buttons} button(s), ` +
          `faces ${uniq(k.fields.map((f) => f.face)).join('/')}, ` +
          `unnamed ${k.fields.filter((f) => !f.named).length}`,
      );
      // …and it sits in the columns the pane already has: the key field on the
      // key column of the mapping's own entries, the value field in the one
      // value column `rhythm.mjs --check` measures.
      const off = k.siblingKeys.filter((s) => s.column !== null && s.composer !== s.column);
      check(
        off.length === 0,
        `${label}  the composer's key field is in the mapping's own key column`,
        off.map((s) => `${s.path} key field at ${s.composer}, column ${s.column}`).join('; ') ||
          k.siblingKeys.map((s) => `${s.path} @ ${s.composer}`).join(', '),
      );
    }

    const NODE_SHAPES = ['0px', '4px', '12px'];
    const strayN = m.nodeRadii.filter((r) => !NODE_SHAPES.includes(r.r));
    check(
      strayN.length === 0,
      `${label}  the graph's node shapes are the three declared kinds`,
      strayN.length > 0
        ? strayN.map((r) => `${r.r} (${r.eg[0]})`).join(', ')
        : m.nodeRadii.map((r) => `${r.r}x${r.n}`).join(' '),
    );
  }

  // ---- 5. The two chrome controls are one control. ----------------------
  //
  // `.button-quiet`'s own comment claims "the same treatment `.toolbar-button`
  // already has". It rendered 17px against 22px. A comment is not a check;
  // this is. Measured on the two panes that carry them.
  {
    const graph = await measure({ scenario: 'stack', theme: 'dark', width: CHECK_WIDTH });
    const df = await measure({ scenario: 'dockerfile', theme: 'dark', width: CHECK_WIDTH });
    const a = graph.chrome.find((c) => c.sel === '.toolbar-button')?.shape;
    const b = df.chrome.find((c) => c.sel === '.button-quiet')?.shape;
    check(
      a !== null && b !== null && JSON.stringify(a) === JSON.stringify(b),
      'chrome  a toolbar control and a header control are the same control',
      `${JSON.stringify(a)} vs ${JSON.stringify(b)}`,
    );
  }

  // ---- 6. Monospace means it is in the file. ----------------------------
  //
  // DESIGN.md's one load-bearing type rule, applied to the pane that broke it:
  // the Dockerfile form rendered `FROM` and `WORKDIR` — the file's own bytes —
  // in the UI face, one click away from a compose pane rendering `NODE_ENV` in
  // the code face. `comment` and `directive` are OUR words and stay in the UI
  // face, so this is not "everything is mono".
  //
  // Falsified by putting `instructionLabelClass` back to `'field-key'`: every
  // keyword reports `ui`.
  for (const theme of ['dark', 'light']) {
    const m = await measure({ scenario: 'dockerfile', theme, width: CHECK_WIDTH });
    const ours = new Set(['comment', 'directive']);
    const keywords = m.keys.filter((k) => /^[A-Z]/.test(k.t));
    const wrong = keywords.filter((k) => k.face !== 'mono');
    check(
      keywords.length > 0 && wrong.length === 0,
      `dockerfile/${theme}  an instruction keyword is in the code face`,
      `${keywords.length} keyword(s)${wrong.length > 0 ? `, ${wrong.map((k) => k.t).join(', ')} in the UI face` : ''}`,
    );
    const prose = m.keys.filter((k) => ours.has(k.t));
    check(
      prose.every((k) => k.face === 'ui'),
      `dockerfile/${theme}  our own word for a row is not`,
      prose.map((k) => `${k.t}:${k.face}`).join(', '),
    );
  }

  // ---- 7. The chrome above the canvas is ONE band. ----------------------
  //
  // The toolbar, the add bar and the legend each carried their own rule, so the
  // 94px between the pane header and the canvas read as three stacked bars
  // against the mockup's single header. Nothing is removed; only the internal
  // rules are, so the band has one edge.
  for (const theme of ['dark', 'light']) {
    const m = await measure({ scenario: 'stack', theme, width: CHECK_WIDTH });
    check(
      m.chromeRules === 1,
      `stack/${theme}  the chrome above the canvas has one edge`,
      `${m.chromeRules} rule(s)`,
    );
    // …and the band does not GROW. The toolbar is one row of controls; a
    // second row is 28px of graph, and it is bought by four pixels of padding
    // nobody would think to measure. Falsified by putting `.toolbar-button`
    // back to `padding: 2px 8px`: two rows, 122px, exit 1.
    check(
      m.toolbarWrapped === false && m.chrome_px !== null && m.chrome_px <= 96,
      `stack/${theme}  the chrome above the canvas stays one toolbar row`,
      `${m.toolbarWrapped ? 'wrapped' : 'one row'}, ${m.chrome_px}px of the pane`,
    );
  }

  console.log(failed === 0 ? '\nthe pane reads as one design' : `\n${failed} check(s) failed`);
} else {
  const out = await measure({
    scenario: process.argv[2] || 'service',
    theme: process.argv[3] || 'dark',
    open: process.argv.includes('--open'),
  });
  console.log(JSON.stringify(out, null, 1));
}

await browser.close();
server.close();
process.exit(failed === 0 ? 0 : 1);
