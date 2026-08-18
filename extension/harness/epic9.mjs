// The three affordances Epic 9 adds, rendered — instrument, and CHECK.
//
//   node harness/epic9.mjs [theme]        # dump what each affordance measures
//   node harness/epic9.mjs --check        # exits 1
//   node harness/epic9.mjs --shots [dir]  # Dark+ and Light+ captures
//
// `rhythm.mjs --check` measures the pane as it arrives. Every control this epic
// adds is behind a PRESS — a collapsed list opens, a comment block opens, a
// move opens — so none of them is on screen when that script measures, and none
// of them would be caught by it. This is its sibling: same page, same shipped
// bundle, same two default themes, but it drives the gesture first and then
// measures what the gesture produced.
//
// The properties it refuses to let regress, each of which is a rendered fact:
//
//   * the owner's own row — `CMD · wget · -qO- · http://localhost:3000/healthz`
//     — is a run of CONTROLS, and pressing one puts a field with the caret in it
//     at that entry's index. The defect being guarded is the one they reported:
//     the row on screen and no way into it;
//   * an expanded list keeps the pane's ONE value column. Entry fields are a
//     nesting level deeper than the rows around them, and the whole of story
//     4.5's geometry is that a nesting level comes out of the key column rather
//     than moving the row;
//   * a comment block puts BOTH positions on screen, in the shapes the engine
//     accepts: a textarea for the run above, one line for the trailing one;
//   * a move shows TWO diffs, both inside the pane's own rectangle. A two-file
//     operation showing one diff is the lie DECISIONS.md 25 exists to prevent,
//     and a second diff scrolled out of the viewport is that lie with extra
//     steps;
//   * the same gesture on a DOCKERFILE shows ONE diff — it writes one file —
//     and says which scope the `ARG` landed in, why it could not be anywhere
//     else, and that nothing feeds it from compose. Placement is the
//     correctness condition of that operation (DECISIONS.md 27), so a block
//     that renders the diff and drops the sentences is a rule the reader cannot
//     check. It is driven on the two-stage fixture, at the SECOND stage's FROM,
//     because a one-stage file cannot tell a global ARG from a stage-scoped
//     one.
import http from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
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
 * The two widths every check runs at, and why these two.
 *
 * The stylesheet designs THREE bands, and until now the harness only ever
 * measured one of them. `main.ts` drops the canvas and gives the panel to the
 * inspector below 600px (`NARROW_PX`); `style.css` collapses the two-column
 * field grid to one below 900px. So 600–900 is a band with its own layout, the
 * only band no scenario here ever entered, and every Epic 9 affordance was
 * unusable inside it for exactly that reason.
 *
 * WIDE_PX 1100 is what every capture and every existing check was taken at, and
 * it stays the reference: the columns a narrow run measures are compared to the
 * wide run's, so the wide number has to keep being produced.
 *
 * NARROW_PX 760 is the middle of the 600–900 band, ~160px clear of both edges.
 * The edges are deliberately NOT the check width. 899 passes the moment anyone
 * moves the breakpoint down, and 610 is confounded by the sub-600 reflow that
 * `.is-narrow` performs — a failure there would not say which of the two
 * layouts broke. A midpoint fails on the rule, not on the boundary. It is also
 * the width `comboshot.mjs` puts the popup at, so the two scripts disagree in
 * the same place when this regresses.
 */
const WIDE_PX = 1100;
const NARROW_PX = 760;

/** The list the owner pointed at, and the value on it they could not edit. */
const LIST = 'services.web.healthcheck.test';
/** A scalar with a comment position and a literal worth moving into a variable. */
const SCALAR = 'services.web.image';

async function openPane(theme, width = WIDE_PX) {
  const page = await browser.newPage();
  await page.setViewport({ width, height: 780 });
  await page.goto(`${base}?scenario=service&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });
  return page;
}

/**
 * The host's two late answers, as fixed values.
 *
 * The harness has no core, and both of these arrive on their own AFTER the pane
 * is drawn — a page that never received one would render the waiting state, and
 * this script would be measuring a sentence rather than a form. Fixed rather
 * than fetched, for the same reason `adapter.ts` fixes the Docker Hub answers:
 * a picture whose contents depend on what a filesystem happened to hold is a
 * different picture every time it is taken. The SHAPES are the shipped wire
 * types, so a renamed field breaks this the way it breaks the pane.
 */
const COMMENTS_ANSWER = {
  above: 'why this image is pinned\nand who asked for it',
  trailing: 'pinned by ops',
};

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
 * The gesture that produces each affordance, run in the page.
 *
 * Each one is the reader's own sequence — press, then the host's answer — and
 * each ends by scrolling the thing it opened into view, so the capture is of
 * the feature rather than of whatever happened to be at the top of the pane.
 */
const GESTURES = {
  list: async (list) => {
    const settle = () => new Promise((r) => setTimeout(r, 80));
    const body = [...document.querySelectorAll('.inspector-body')].find(
      (e) => e.getBoundingClientRect().height > 0,
    );
    body.querySelectorAll(`[data-path="${list}"] .field-item`)[1].click();
    await settle();
    body.querySelector(`[data-field="${list}[0]"]`).scrollIntoView({ block: 'center' });
    await settle();
  },
  comment: async (_list, scalar, _extract, comments) => {
    const settle = () => new Promise((r) => setTimeout(r, 80));
    const body = [...document.querySelectorAll('.inspector-body')].find(
      (e) => e.getBoundingClientRect().height > 0,
    );
    body.querySelector(`.comment-open[data-path="${scalar}"]`).click();
    await settle();
    window.postMessage({ type: 'comments', file: 'x', path: scalar, staged: [], ...comments }, '*');
    await settle();
    body.querySelector('.comment-block').scrollIntoView({ block: 'center' });
    await settle();
  },
  move: async (_list, scalar, extract) => {
    const settle = () => new Promise((r) => setTimeout(r, 80));
    const body = [...document.querySelectorAll('.inspector-body')].find(
      (e) => e.getBoundingClientRect().height > 0,
    );
    body.querySelector(`.extract-open[data-path="${scalar}"]`).click();
    await settle();
    window.postMessage(
      { type: 'extract', file: 'x', path: scalar, staged: false, result: extract },
      '*',
    );
    await settle();
    body.querySelector('.extract-block').scrollIntoView({ block: 'center' });
    await settle();
  },
};

/** Where a value's ink starts — the same measurement `rhythm.mjs` makes. */
const INK = `(el) => {
  const r = el.getBoundingClientRect();
  return +(r.left + parseFloat(getComputedStyle(el).paddingLeft)).toFixed(1);
}`;

/**
 * Everything the three affordances put on screen, after the gestures that
 * produce them.
 *
 * The two `postMessage` calls are the HOST's answers, which the harness has no
 * core to fetch: `comments` and `extract` both arrive late and on their own, so
 * a page that never received one would render the waiting state and this script
 * would measure a sentence rather than a form. They are the shipped wire shapes
 * — a renamed field breaks this the way it breaks the pane.
 */
async function measure(theme, width = WIDE_PX) {
  const page = await openPane(theme, width);
  const out = await page.evaluate(
    async (list, scalar, inkSrc, said, extract) => {
      const ink = eval(inkSrc);
      const settle = () => new Promise((r) => setTimeout(r, 60));
      const body = [...document.querySelectorAll('.inspector-body')].find(
        (e) => e.getBoundingClientRect().height > 0,
      );
      const paneBox = body.getBoundingClientRect();
      const visible = (el) => {
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0 && r.top >= paneBox.top - 1 && r.bottom <= paneBox.bottom + 1;
      };
      const columns = () =>
        [...new Set(
          [...body.querySelectorAll('.field > .field-value')]
            .filter((e) => e.getBoundingClientRect().width > 0)
            .map((e) => Math.round(ink(e))),
        )];
      /**
       * How many grid tracks each visible `.field` in the pane actually has.
       *
       * This is the narrow band's form of `columns()`. Below 900px there is no
       * key column to share, so "one value column" is not a property of this
       * layout — nesting genuinely moves the row, because the key column it
       * would otherwise come out of does not exist. Asserting it here would be
       * asserting the wide design against a layout that deliberately dropped
       * it.
       *
       * What IS the property is that the media query reached every field. The
       * wide grid is four tracks (key, value, mark, actions); the narrow grid
       * is three (value, mark, actions) with the key spanning the row above.
       * A field still holding four tracks below 900px is one the media query
       * missed — and since the key still spans `1 / -1`, its value is squeezed
       * into the leftover key track. That is exactly D6: the move block's name
       * field rendered 72px wide with 200px of its own row empty beside it.
       *
       * A track count is the mechanism itself rather than a pixel downstream of
       * it, so this cannot pass by coincidence the way an x-position can.
       */
      const trackCounts = () =>
        [...new Set(
          [...body.querySelectorAll('.field')]
            .filter((e) => e.getBoundingClientRect().width > 0)
            .map((e) => getComputedStyle(e).gridTemplateColumns.split(/\s+/).length),
        )].sort();
      /**
       * Anything sticking out of the pane's left or right edge.
       *
       * The narrow band's form of `visible()`. `visible()` asks whether an
       * element fits the pane VERTICALLY, which at 760 is a question about the
       * viewport height the harness happened to choose — a 302px-wide pane
       * makes every block taller, and nothing in the stylesheet can fix that.
       * Sideways overflow is the constraint this band actually imposes, and it
       * is the one `main.ts` states: "the page never scrolls sideways".
       */
      const spills = (root) =>
        [...root.querySelectorAll('*')]
          .filter((e) => {
            const r = e.getBoundingClientRect();
            return r.width > 0 && r.height > 0 && (r.left < paneBox.left - 1 || r.right > paneBox.right + 1);
          })
          .map((e) => `${e.tagName.toLowerCase()}.${(e.className || '').toString().split(' ')[0]}`);

      /**
       * Every line that hangs off the key column, and where it actually starts.
       *
       * `.prov`, `.resolution`, `.diag`, `.allowed-note` and the combo popup
       * are all positioned with `calc(var(--key-col) - var(--ind))`. Below
       * 900px there is no key column, so each of them must be reset to zero —
       * the narrow section resets the first three and MISSED the last two,
       * which is how `.allowed-note` came to start a key column inside a pane
       * that no longer has one. Reading the resolved offset rather than the
       * rule catches the case the section could not reset from where it sits:
       * a rule declared later in the file wins on source order at equal
       * specificity, which is exactly what happened to both of the missed two.
       */
      const hangers = () =>
        [...body.querySelectorAll('.prov, .resolution, .diag, .allowed-note, .combo-popup')]
          .filter((e) => e.getBoundingClientRect().width > 0)
          .map((e) => {
            const cs = getComputedStyle(e);
            const off = Math.round(parseFloat(cs.marginLeft) || 0) + Math.round(parseFloat(cs.left) || 0);
            return off === 0 ? null : `${(e.className || '').toString().split(' ').pop()}=${off}`;
          })
          .filter(Boolean);

      // ---- the collapsed list, before anything is pressed -----------------
      const block = body.querySelector(`[data-path="${list}"]`);
      const items = [...block.querySelectorAll('.field-item')];
      const collapsed = {
        text: items.map((e) => e.textContent),
        tags: [...new Set(items.map((e) => e.tagName))],
        named: items.map((e) => e.getAttribute('aria-label') || ''),
        columnsBefore: columns(),
      };

      // ---- press the entry in the MIDDLE ----------------------------------
      // `wget`, index 1. The one the owner wanted to change.
      items[1].click();
      await settle();
      const fields = [...body.querySelectorAll('[data-field]')]
        .map((e) => e.dataset.field)
        .filter((f) => f.startsWith(`${list}[`));
      const expanded = {
        fields,
        focused: document.activeElement ? document.activeElement.dataset.field : null,
        columnsAfter: columns(),
        tracksAfter: trackCounts(),
        removes: body.querySelectorAll('.entry-remove').length,
        addField: fields.filter((f) => f.endsWith('[+]')).length,
      };

      // ---- the comment block ----------------------------------------------
      const commentControl = body.querySelector(`.comment-open[data-path="${scalar}"]`);
      commentControl.click();
      await settle();
      window.postMessage(
        { type: 'comments', file: 'x', path: scalar, staged: [], above: said.above, trailing: said.trailing },
        '*',
      );
      await settle();
      const above = body.querySelector(`[data-field="comment:above:${scalar}"]`);
      const trailing = body.querySelector(`[data-field="comment:trailing:${scalar}"]`);
      const comments = {
        aboveTag: above ? above.tagName : null,
        trailingTag: trailing ? trailing.tagName : null,
        aboveVisible: above ? visible(above) : false,
        trailingVisible: trailing ? visible(trailing) : false,
        aboveHeight: above ? Math.round(above.getBoundingClientRect().height) : 0,
        removes: body.querySelectorAll('.comment-remove').length,
        columns: columns(),
        tracks: trackCounts(),
        hangers: hangers(),
        spills: spills(body.querySelector('.comment-block') || body),
      };
      commentControl.click();
      await settle();

      // ---- the move --------------------------------------------------------
      const extractControl = body.querySelector(`.extract-open[data-path="${scalar}"]`);
      extractControl.click();
      await settle();
      window.postMessage(
        { type: 'extract', file: 'x', path: scalar, staged: false, result: extract },
        '*',
      );
      await settle();
      const diffs = [...body.querySelectorAll('.extract-diff')];
      const move = {
        diffs: diffs.length,
        visible: diffs.map((d) => visible(d)),
        text: diffs.map((d) => (d.textContent || '').replace(/\s+/g, ' ').trim()),
        stage: (body.querySelector('.extract-stage') || {}).textContent || null,
        stageVisible: body.querySelector('.extract-stage')
          ? visible(body.querySelector('.extract-stage'))
          : false,
        name: (body.querySelector(`[data-field="extract:${scalar}"]`) || {}).value ?? null,
        note: [...body.querySelectorAll('.extract-block .field-note')].map((n) => n.textContent),
        columns: columns(),
        tracks: trackCounts(),
        spills: spills(body.querySelector('.extract-block') || body),
      };

      // ---- the strip, with the move staged ---------------------------------
      // `Save to <file>` is no longer literally one file, and DECISIONS.md 25
      // says the preview carries both diffs BECAUSE of that. The strip is where
      // the reader presses it, so the second diff has to be there too.
      window.postMessage(
        {
          type: 'pending',
          file: '/w/compose.yaml',
          count: 1,
          diff: extract.compose.diff,
          added: 1,
          removed: 1,
          saveLabel: 'Save to compose.yaml and .env',
          env: {
            file: extract.env_file,
            diff: extract.env_diff,
            note: 'This creates .env beside the compose file and puts WEB_IMAGE in it.',
          },
        },
        '*',
      );
      await settle();
      const strip = document.querySelector('.pending');
      const stripBox = strip.getBoundingClientRect();
      const inStrip = (el) => {
        const r = el.getBoundingClientRect();
        return r.height > 0 && r.top >= stripBox.top - 1 && r.bottom <= stripBox.bottom + 1;
      };
      const secondBody = strip.querySelector('.pending-second .pending-diff');
      const pending = {
        save: (strip.querySelector('.button-primary') || {}).textContent || null,
        secondShown: secondBody ? inStrip(secondBody) : false,
        secondText: secondBody ? (secondBody.textContent || '').replace(/\s+/g, ' ').trim() : null,
        note: (strip.querySelector('.pending-second-label') || {}).textContent || null,
      };

      // Every control this epic adds carries an accessible name — the floor
      // story 4.5 set, applied to controls that only exist after a gesture and
      // are therefore invisible to a scan of the initial render.
      const unnamed = [...body.querySelectorAll('button, input, textarea')]
        .filter((e) => {
          const name = (e.getAttribute('aria-label') || e.textContent || '').trim();
          return name === '';
        })
        .map((e) => e.className);

      return { collapsed, expanded, comments, move, pending, unnamed };
    },
    LIST,
    SCALAR,
    INK,
    COMMENTS_ANSWER,
    EXTRACT_ANSWER,
  );
  await page.close();
  return out;
}

/**
 * The instruction the ARG move is driven at: the SECOND stage's `FROM`, in
 * `harness/fixtures/core.json`'s two-stage Dockerfile.
 *
 * A one-stage fixture would pass every check below while proving nothing: the
 * whole of story 9.4 is that this declaration goes above the FIRST `FROM` and
 * not above the one it came out of.
 */
const ARG_AT = 15;

const EXTRACT_ARG_ANSWER = {
  name: 'NGINX_VERSION',
  value: '1.27-alpine',
  dockerfile: {
    file: '/w/webstack/docs/Dockerfile',
    ops: [],
    diff:
      '--- a/Dockerfile\n+++ b/Dockerfile\n@@ -1,6 +1,7 @@\n+ARG NGINX_VERSION=1.27-alpine\n' +
      ' FROM node:18-alpine AS build\n@@ -16,2 +17,2 @@\n-FROM nginx:1.27-alpine AS runtime\n' +
      '+FROM nginx:${NGINX_VERSION} AS runtime\n',
    added: 2,
    removed: 1,
    changed_lines: 3,
    written: false,
  },
  scope: 'global',
  scope_reason:
    'a FROM can only use an ARG declared before the FIRST FROM, so the declaration went above ' +
    'line 6 and could not go anywhere else — inside a stage it would expand to the empty string ' +
    'with no error',
  arg_line: 'ARG NGINX_VERSION=1.27-alpine',
  declared: true,
  redeclared: false,
  already_declared: false,
  compose_note:
    'Nothing feeds `NGINX_VERSION` from compose. `docker compose` passes build arguments only ' +
    'through `build.args`, so add `NGINX_VERSION: ${NGINX_VERSION}` under the service yourself.',
  written: false,
};

/**
 * The Dockerfile half of the move — story 9.4 — rendered and measured.
 *
 * A page of its own because it is a different VIEW: the stage form replaces the
 * stack, and the gesture is addressed by an instruction index rather than by a
 * config path.
 */
async function measureArg(theme, width = WIDE_PX) {
  const page = await browser.newPage();
  await page.setViewport({ width, height: 780 });
  await page.goto(`${base}?scenario=dockerfile&theme=${theme}`, { waitUntil: 'networkidle0' });
  await page.waitForFunction('window.__ready === true', { timeout: 15000 });
  const out = await page.evaluate(
    async (at, inkSrc, answer) => {
      const ink = eval(inkSrc);
      const settle = () => new Promise((r) => setTimeout(r, 60));
      const body = [...document.querySelectorAll('.inspector-body')].find(
        (e) => e.getBoundingClientRect().height > 0,
      );
      const paneBox = body.getBoundingClientRect();
      const visible = (el) => {
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0 && r.top >= paneBox.top - 1 && r.bottom <= paneBox.bottom + 1;
      };
      const columns = () =>
        [...new Set(
          [...body.querySelectorAll('.field > .field-value')]
            .filter((e) => e.getBoundingClientRect().width > 0)
            .map((e) => Math.round(ink(e))),
        )];
      // Same two band-aware measures as `measure()` above — see the comments
      // on `trackCounts` and `spills` there for why the narrow band needs them
      // instead of `columns()` and `visible()`.
      const trackCounts = () =>
        [...new Set(
          [...body.querySelectorAll('.field')]
            .filter((e) => e.getBoundingClientRect().width > 0)
            .map((e) => getComputedStyle(e).gridTemplateColumns.split(/\s+/).length),
        )].sort();
      const spills = (root) =>
        [...root.querySelectorAll('*')]
          .filter((e) => {
            const r = e.getBoundingClientRect();
            return r.width > 0 && r.height > 0 && (r.left < paneBox.left - 1 || r.right > paneBox.right + 1);
          })
          .map((e) => `${e.tagName.toLowerCase()}.${(e.className || '').toString().split(' ')[0]}`);
      const columnsBefore = columns();

      const control = body.querySelector(`.extract-open[data-instruction="${at}"]`);
      if (control === null) {
        return { offered: false, columnsBefore };
      }
      control.click();
      await settle();
      window.postMessage(
        { type: 'extractArg', file: '/w/Dockerfile', instruction: at, staged: false, result: answer },
        '*',
      );
      await settle();
      const block = body.querySelector('.extract-block');
      if (block) {
        block.scrollIntoView({ block: 'center' });
        await settle();
      }
      const diffs = [...body.querySelectorAll('.extract-diff')];
      const notes = [...body.querySelectorAll('.extract-block .field-note')].map((n) => n.textContent);
      const stage = body.querySelector('.extract-stage');
      const name = body.querySelector(`[data-field="extract-arg:${at}"]`);
      const unnamed = [...body.querySelectorAll('button, input, textarea')]
        .filter((e) => ((e.getAttribute('aria-label') || e.textContent || '').trim()) === '')
        .map((e) => e.className);
      return {
        offered: true,
        columnsBefore,
        diffs: diffs.length,
        diffVisible: diffs.map((d) => visible(d)),
        diffText: diffs.map((d) => (d.textContent || '').replace(/\s+/g, ' ').trim()),
        notes,
        stage: stage ? stage.textContent : null,
        stageVisible: stage ? visible(stage) : false,
        name: name ? name.value : null,
        columns: columns(),
        tracks: trackCounts(),
        spills: spills(block || body),
        unnamed,
      };
    },
    ARG_AT,
    INK,
    EXTRACT_ARG_ANSWER,
  );
  await page.close();
  return out;
}

let failed = 0;
const check = (ok, label, detail = '') => {
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${label}${detail ? `  ${detail}` : ''}`);
  if (!ok) failed++;
};

if (process.argv.includes('--shots')) {
  const dir =
    process.argv[process.argv.indexOf('--shots') + 1] ||
    process.env.SHOTS ||
    path.join(ext, '..', 'shots-epic9');
  await mkdir(dir, { recursive: true });
  // One capture per affordance rather than one per theme, and each is the
  // INSPECTOR's own rectangle scrolled to the thing that was opened: a single
  // full-panel shot puts two of the three below the fold, which is a picture of
  // the canvas with a feature somewhere underneath it.
  for (const theme of ['dark', 'light']) {
    const suffix = theme === 'dark' ? 'dark-plus' : 'light-plus';
    for (const [name, open] of Object.entries(GESTURES)) {
      const page = await openPane(theme);
      await page.evaluate(open, LIST, SCALAR, EXTRACT_ANSWER, COMMENTS_ANSWER);
      const pane = await page.$('.inspector');
      const shot = path.join(dir, `epic9-${name}-${suffix}.png`);
      await pane.screenshot({ path: shot });
      console.log(`wrote ${shot}`);
      await page.close();
    }
  }
} else if (process.argv.includes('--check')) {
  for (const width of [WIDE_PX, NARROW_PX]) {
  // Which geometry is a property of THIS band. The affordances themselves —
  // what is a control, what it is called, which entry the caret lands in, what
  // the diff says — are the same facts at every width and are checked at every
  // width. Only the two questions that are answered by the layout change:
  //
  //   the value column   wide: every value shares one x, because a nesting
  //                            level comes out of the key column (story 4.5)
  //                    narrow: there is no key column to come out of, so the
  //                            question is whether the one-column grid reached
  //                            every field — a four-track field below 900px is
  //                            one the media query missed, and its value is
  //                            squeezed into the leftover key track
  //
  //   inside the pane    wide: fits vertically, so both diffs can be read
  //                            without hunting for the second one
  //                    narrow: does not spill sideways. Vertical fit in a
  //                            302px-wide pane is a fact about the harness's
  //                            chosen viewport height, not about the stylesheet
  const narrow = width <= 900;
  const WIDE_TRACKS = 4;
  const NARROW_TRACKS = 3;
  const tracksOk = (t) => t.length === 1 && t[0] === (narrow ? NARROW_TRACKS : WIDE_TRACKS);
  for (const theme of ['dark', 'light']) {
    const m = await measure(theme, width);
    const label = `epic9/${theme}@${width}`;

    // The owner's row, and the four words they wrote about it.
    check(
      m.collapsed.tags.length === 1 && m.collapsed.tags[0] === 'BUTTON',
      `${label}  every entry of a collapsed list is a control`,
      `tags ${m.collapsed.tags.join(', ')} on ${JSON.stringify(m.collapsed.text)}`,
    );
    check(
      m.collapsed.named.every((n, i) => n.includes(`[${i}]`)),
      `${label}  a collapsed entry announces which index it is`,
      JSON.stringify(m.collapsed.named[1]),
    );
    // Pressing the MIDDLE entry. With `CMD · wget · -qO- · …` an off-by-one
    // would open the list at the wrong entry, and the caret is the only thing
    // on screen that says which one it opened at.
    check(
      m.expanded.focused === `${LIST}[1]`,
      `${label}  pressing an entry opens the list with the caret in THAT entry`,
      `caret in ${m.expanded.focused}`,
    );
    check(
      m.expanded.fields.length === 5,
      `${label}  an expanded list has a field per entry and one to add with`,
      `fields ${JSON.stringify(m.expanded.fields)}`,
    );
    check(
      m.expanded.removes === 4,
      `${label}  every entry can be removed`,
      `${m.expanded.removes} controls for 4 entries`,
    );
    // Story 4.5's geometry, which is exactly what a new nesting level breaks.
    check(
      narrow ? tracksOk(m.expanded.tracksAfter) : m.expanded.columnsAfter.length === 1,
      narrow
        ? `${label}  an expanded list gets the one-column grid on every field`
        : `${label}  an expanded list keeps the pane's one value column`,
      narrow ? `tracks = ${m.expanded.tracksAfter.join(', ')}` : `x = ${m.expanded.columnsAfter.join(', ')}`,
    );

    // Both positions, in the shapes the engine accepts.
    check(
      m.comments.aboveTag === 'TEXTAREA' && m.comments.trailingTag === 'INPUT',
      `${label}  a comment block offers both positions, in the shape each one is`,
      `above ${m.comments.aboveTag}, trailing ${m.comments.trailingTag}`,
    );
    check(
      narrow ? m.comments.spills.length === 0 : m.comments.aboveVisible && m.comments.trailingVisible,
      narrow
        ? `${label}  a comment block does not spill out of the pane sideways`
        : `${label}  both comment fields are inside the pane`,
      narrow
        ? m.comments.spills.join(', ')
        : `above ${m.comments.aboveVisible}, trailing ${m.comments.trailingVisible}`,
    );
    // Below 900px there is no key column, so nothing may still be indented to
    // one. Narrow-band only: above 900px a non-zero offset is the CORRECT
    // answer, and this would be asserting the wrong layout.
    if (narrow) {
      check(
        m.comments.hangers.length === 0,
        `${label}  no subordinate line is still indented to a key column that is gone`,
        m.comments.hangers.join(', '),
      );
    }
    // A run of comment lines is ONE comment, so the field for it has to have
    // room for more than one — a single-line box would say the opposite.
    check(
      m.comments.aboveHeight >= 40,
      `${label}  the field for a run of comment lines has room for a run`,
      `${m.comments.aboveHeight}px`,
    );
    check(
      m.comments.removes === 2,
      `${label}  a position with a comment offers a way to remove it`,
      `${m.comments.removes} of 2`,
    );
    check(
      narrow ? tracksOk(m.comments.tracks) : m.comments.columns.length === 1,
      narrow
        ? `${label}  a comment block gets the one-column grid on every field`
        : `${label}  a comment block keeps the pane's one value column`,
      narrow ? `tracks = ${m.comments.tracks.join(', ')}` : `x = ${m.comments.columns.join(', ')}`,
    );

    // DECISIONS.md 25: a two-file operation that showed one diff would be a lie
    // about the half the reader cannot see — and a diff outside the pane's
    // rectangle is not shown.
    check(
      m.move.diffs === 2 && (narrow ? m.move.spills.length === 0 : m.move.visible.every(Boolean)),
      `${label}  a move shows BOTH diffs, both inside the pane`,
      `${m.move.diffs} diff(s), visible ${JSON.stringify(m.move.visible)}`,
    );
    check(
      /\$\{WEB_IMAGE\}/.test(m.move.text[0] ?? '') && /WEB_IMAGE=/.test(m.move.text[1] ?? ''),
      `${label}  the compose half and the .env half are the two that are shown`,
      JSON.stringify(m.move.text.map((t) => t.slice(0, 70))),
    );
    check(
      m.move.name === 'WEB_IMAGE',
      `${label}  the suggested name is in a field the reader can change`,
      JSON.stringify(m.move.name),
    );
    check(
      m.move.note.some((t) => /creates/i.test(t) && t.includes('.env')),
      `${label}  the pane says the .env would be created rather than appended to`,
      JSON.stringify(m.move.note),
    );
    check(
      narrow ? tracksOk(m.move.tracks) : m.move.columns.length === 1,
      narrow
        ? `${label}  a move block gets the one-column grid on every field`
        : `${label}  a move block keeps the pane's one value column`,
      narrow ? `tracks = ${m.move.tracks.join(', ')}` : `x = ${m.move.columns.join(', ')}`,
    );
    check(
      m.move.stage !== null && (narrow ? m.move.spills.length === 0 : m.move.stageVisible),
      `${label}  the move is staged by a control the reader can see and press`,
      JSON.stringify(m.move.stage),
    );

    // The strip the reader presses Save in.
    check(
      /compose\.yaml and \.env/.test(m.pending.save ?? ''),
      `${label}  the write control names BOTH files it would write`,
      JSON.stringify(m.pending.save),
    );
    check(
      m.pending.secondShown && /WEB_IMAGE=/.test(m.pending.secondText ?? ''),
      `${label}  the strip carries the .env diff, inside its own box`,
      `${m.pending.secondShown} ${JSON.stringify((m.pending.secondText ?? '').slice(0, 50))}`,
    );

    // N6, on the controls a scan of the initial render cannot reach.
    check(
      m.unnamed.length === 0,
      `${label}  every control these gestures produce has an accessible name`,
      m.unnamed.join(', '),
    );

    // ---- story 9.4: the same gesture in the other grammar ----------------
    const a = await measureArg(theme, width);
    check(
      a.offered === true,
      `${label}  the second stage's FROM offers the move into a build argument`,
      `instruction ${ARG_AT}`,
    );
    if (a.offered) {
      // ONE file. A second diff here would name a file this operation does not
      // write, which is DECISIONS.md 27 read backwards.
      check(
        a.diffs === 1 && (narrow ? a.spills.length === 0 : a.diffVisible.every(Boolean)),
        `${label}  the one-file move shows ONE diff, inside the pane`,
        `${a.diffs} diff(s), visible ${JSON.stringify(a.diffVisible)}`,
      );
      check(
        /ARG NGINX_VERSION=1\.27-alpine/.test(a.diffText[0] ?? ''),
        `${label}  the declaration the reader would write is in the diff`,
        JSON.stringify((a.diffText[0] ?? '').slice(0, 80)),
      );
      // The placement rule, in the core's own sentence. A rule the reader
      // cannot see is a rule they cannot check.
      check(
        a.notes.some((t) => t.includes(EXTRACT_ARG_ANSWER.scope_reason)),
        `${label}  the block carries the core's reason for the scope, verbatim`,
        JSON.stringify(a.notes.map((t) => t.slice(0, 60))),
      );
      check(
        a.notes.some((t) => /global scope/.test(t)),
        `${label}  the block says which scope the ARG landed in`,
        JSON.stringify(a.notes.map((t) => t.slice(0, 40))),
      );
      // `build.args` is deliberately not wired. Losing this leaves the reader
      // with an ARG nothing supplies.
      check(
        a.notes.some((t) => t.includes(EXTRACT_ARG_ANSWER.compose_note)),
        `${label}  the block says nothing feeds the argument from compose`,
        JSON.stringify(a.notes.map((t) => t.slice(0, 40))),
      );
      check(
        a.name === 'NGINX_VERSION',
        `${label}  the suggested argument name is in a field the reader can change`,
        JSON.stringify(a.name),
      );
      check(
        a.stage !== null && (narrow ? a.spills.length === 0 : a.stageVisible),
        `${label}  the move is staged by a control the reader can see and press`,
        JSON.stringify(a.stage),
      );
      // Story 4.5's geometry, on the pane that had no move block until now.
      check(
        narrow ? tracksOk(a.tracks) : a.columns.length === 1 && a.columns[0] === a.columnsBefore[0],
        narrow
          ? `${label}  the block gets the one-column grid on every field`
          : `${label}  the block keeps the Dockerfile pane's one value column`,
        narrow
          ? `tracks = ${a.tracks.join(', ')}`
          : `x = ${a.columns.join(', ')} (was ${a.columnsBefore.join(', ')})`,
      );
      check(
        a.unnamed.length === 0,
        `${label}  every control this gesture produces has an accessible name`,
        a.unnamed.join(', '),
      );
    }
  }
  }
  console.log(
    failed === 0
      ? '\nthe list, the comment and both moves are all reachable and all readable'
      : `\n${failed} check(s) failed`,
  );
} else {
  const out = await measure(process.argv[2] || 'dark');
  console.log(JSON.stringify(out, null, 1));
  await writeFile('/dev/null', '');
}

await browser.close();
server.close();
process.exit(failed === 0 ? 0 : 1);
