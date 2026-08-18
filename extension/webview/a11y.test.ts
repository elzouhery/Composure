// The accessibility floor — story 4.5.
//
// The deliverable of that story is not a feature. It is THIS FILE: a set of
// checks that fail when the floor erodes. 4.1 and 4.2 built the floor
// opportunistically and nothing tracked it; Epics 5 and 6 then added five
// surfaces to it. Everything below scans the shipped sources rather than
// listing what exists today, so a control written next year is checked by the
// same pass.
//
// What is NOT claimed anywhere in here: that anything was rendered, or that a
// contrast ratio was measured. This process cannot do either. See
// MANUAL_CONTRAST_CHECKS, printed by the last test in this file and repeated
// in TESTING.md, for what a human must then check by eye.

import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  DERIVED_EXCEPTIONS,
  GUARANTEED_ON,
  INK_ON,
  RAISED_SURFACES,
  MANUAL_CONTRAST_CHECKS,
  NON_TEXT_SURFACES,
  OVERLAY_BACKGROUNDS,
  SEVERITY_CARRIERS,
  SHAPE_FILL_SELECTORS,
  SEVERITY_TOKENS,
  auditSource,
  derivedPairings,
  elementBindings,
  hasAccessibleName,
  inkDeclarations,
  inkTokens,
  isExcepted,
  isGuaranteedPairing,
  isKeyboardReachable,
  outlineGaps,
  paintChain,
  separates,
  shippedSources,
  stripComments,
  surfaceTokens,
  tokenDeclarations,
  underivedInk,
} from './a11y';
import { markerIndex, severityBadge } from './layout';
import { pillName, pillText, pillTitle, severityWord } from './inspector';

/**
 * The extension root, wherever the tests were invoked from.
 *
 * `npm --prefix extension test` does not run from extension/, so a cwd-relative
 * path passes locally and fails from the repo root. A check that depends on
 * where it was started is not a check.
 */
const ROOT = [process.cwd(), resolve(process.cwd(), 'extension')].find((b) =>
  existsSync(resolve(b, 'webview/style.css')),
);
assert.ok(ROOT, 'could not locate the extension sources');

/**
 * Every shipped webview source, ENUMERATED from the directory.
 *
 * This used to be a hardcoded array of seven names while the file's own header
 * claimed it scanned the shipped sources. Everything written after story 4.5 —
 * including `webview/groups.ts`, added by the change that fixed this — was
 * outside the scan by construction, so a new file with an unnamed button and a
 * pointer-only div passed the accessibility floor clean.
 */
const WEBVIEW_SOURCES = shippedSources(readdirSync(resolve(ROOT!, 'webview'))).map(
  (name) => `webview/${name}`,
);

const read = (relative: string): string => readFileSync(resolve(ROOT!, relative), 'utf8');
const CSS = read('webview/style.css');

/* -------------------------------------------------------------------------
 * Every control reachable, every control named.
 * ---------------------------------------------------------------------- */

describe('every interactive element is reachable and named', () => {
  // This is the criterion in full: every node, every control and every
  // focusable element, including everything Epics 5 and 6 added — the
  // inspector's fields, the `available, not set` list, the provenance buttons,
  // the pending strip's Save and Discard, and the Dockerfile stage form.
  it('finds no pointer-only capability and no unnamed control in the shipped webview', () => {
    const defects = WEBVIEW_SOURCES.flatMap((file) => auditSource(file, read(file)));
    assert.deepEqual(
      defects,
      [],
      `accessibility defects:\n${defects
        .map((d) => `  ${d.file}:${d.line} ${d.element} ${d.problem}`)
        .join('\n')}`,
    );
  });

  // The scan's REACH, which is a separate property from what it detects. A
  // perfect detector pointed at seven of nine files is a check that cannot
  // fail on the other two.
  it('scans every shipped webview source, including ones added after this test', () => {
    const onDisk = readdirSync(resolve(ROOT!, 'webview'));
    const shipped = onDisk.filter(
      (n) => n.endsWith('.ts') && !n.endsWith('.test.ts') && !n.endsWith('.d.ts'),
    );
    assert.ok(shipped.length >= 8, `only ${shipped.length} webview sources found`);
    assert.deepEqual(
      WEBVIEW_SOURCES.map((f) => f.replace('webview/', '')).sort(),
      shipped.sort(),
      'the scan and the directory disagree, so some shipped file is unchecked',
    );
    // Files that exist today and were NOT in the old hardcoded seven. If the
    // enumeration regresses to a literal list, these are what it drops.
    for (const late of ['groups.ts', 'a11y.ts']) {
      assert.ok(
        WEBVIEW_SOURCES.includes(`webview/${late}`),
        `${late} ships and is not scanned`,
      );
    }
  });

  it('would pick up a file nobody has written yet', () => {
    // The filter, applied to a listing that does not exist on disk: the point
    // is that adding a source is enough to be scanned, with no list to update.
    assert.deepEqual(
      shippedSources(['main.ts', 'brandnew.ts', 'main.test.ts', 'style.css', 'x.d.ts']),
      ['brandnew.ts', 'main.ts'],
    );
  });

  // A scan that never fires is indistinguishable from a scan that is broken,
  // and this one has to keep firing on code nobody has written yet.
  it('catches a button with no accessible name', () => {
    const source = `
      const icon = document.createElement('button');
      icon.className = 'x';
      icon.addEventListener('click', () => go());
    `;
    const defects = auditSource('planted.ts', source);
    assert.equal(defects.length, 1);
    assert.match(defects[0].problem, /no accessible name/);
  });

  it('catches a capability wired to a div nobody can focus', () => {
    const source = `
      const row = el('div', 'row');
      row.textContent = 'open';
      row.addEventListener('click', () => open());
    `;
    const defects = auditSource('planted.ts', source);
    assert.equal(defects.length, 1);
    assert.match(defects[0].problem, /pointer-only/);
  });

  it('accepts the same div once it is given a tab stop', () => {
    const source = `
      const row = el('div', 'row');
      row.textContent = 'open';
      row.tabIndex = 0;
      row.addEventListener('click', () => open());
      row.addEventListener('keydown', () => open());
    `;
    assert.deepEqual(auditSource('planted.ts', source), []);
  });

  it('counts a name assembled from named children, and not one assembled from nothing', () => {
    const named = `
      const button = document.createElement('button');
      const label = el('span', 'l');
      label.textContent = 'Retry';
      button.append(label);
    `;
    const unnamed = `
      const button = document.createElement('button');
      const swatch = el('span', 's');
      swatch.setAttribute('aria-hidden', 'true');
      button.append(swatch);
    `;
    assert.equal(hasAccessibleName(named, 'button'), true);
    assert.equal(hasAccessibleName(unnamed, 'button'), false);
  });

  it('reads a roving tabindex as reachable — that is the listbox pattern', () => {
    const source = `
      const group = document.createElementNS(SVG_NS, 'g');
      group.setAttribute('tabindex', '-1');
      group.addEventListener('pointerdown', () => pick());
    `;
    const [binding] = elementBindings(source);
    assert.equal(binding.tag, 'g');
    assert.equal(isKeyboardReachable(source, binding), true);
  });
});

describe('the graph keeps its own keyboard contract', () => {
  // The canvas is the one surface with no native control semantics at all, so its
  // contract is written by hand and is the easiest to lose.
  const graph = read('webview/graph.ts');

  it('is a listbox with a tab stop, an active descendant and a roving tabindex', () => {
    assert.match(graph, /setAttribute\('role', 'listbox'\)/);
    assert.match(graph, /setAttribute\('role', 'option'\)/);
    assert.match(graph, /aria-activedescendant/);
    assert.match(graph, /applyRovingTabIndex/);
  });

  it('gives every node an accessible name that says what it is', () => {
    assert.match(graph, /setAttribute\('aria-label', badge === null \? name : /);
  });

  it('answers Enter the way it answers a click', () => {
    // Whatever a mouse press opens, the keyboard opens — or the canvas is
    // reachable by keyboard and not usable by one.
    assert.match(graph, /e\.key === 'Enter'/);
    assert.match(graph, /this\.activate\(this\.selectedId\)/);
  });

  it('never removes a focus ring', () => {
    for (const file of WEBVIEW_SOURCES) {
      // Comments stripped first: this codebase explains that it never writes
      // `outline: none`, and the explanation says the words.
      const source = stripComments(read(file));
      assert.equal(
        /outline\s*:\s*['"]?none/.test(source),
        false,
        `${file} removes an outline in script`,
      );
    }
    assert.deepEqual(
      outlineGaps(CSS),
      [],
      'a rule removes an outline without restoring one on :focus-visible',
    );
  });

  it('catches an outline removed with nothing put back', () => {
    assert.deepEqual(outlineGaps('.x:focus { outline: none; }'), ['.x:focus']);
    assert.deepEqual(
      outlineGaps('.x:focus { outline: none; } .x:focus-visible { outline: 1px solid red; }'),
      [],
    );
  });

  // Story 4.4's controls are part of the floor from the day they land, not
  // from the day someone notices they are not.
  it('makes search, focus and collapse reachable and named', () => {
    const main = read('webview/main.ts');
    assert.match(main, /this\.search\.setAttribute\('aria-label'/);
    assert.match(main, /this\.focusButton\.setAttribute\('aria-pressed'/);
    // Was: /…String\(this\.collapseBy === by\)\)|aria-pressed/ — an alternation
    // whose right-hand side matches the WORD `aria-pressed` anywhere in the
    // file, which the focus button on the line above already guarantees. The
    // assertion could not fail while the two collapse buttons had no state at
    // all. Each button is now named individually.
    for (const button of ['networkButton', 'profileButton']) {
      assert.match(
        main,
        new RegExp(`this\\.${button}\\.setAttribute\\('aria-pressed'`),
        `${button} does not report its pressed state`,
      );
      assert.match(
        main,
        new RegExp(`this\\.${button}\\.setAttribute\\('aria-label'`),
        `${button} has no accessible name`,
      );
    }
    assert.match(main, /this\.toolbarStatus\.setAttribute\('role', 'status'\)/);
    assert.match(main, /this\.toolbarStatus\.setAttribute\('aria-live', 'polite'\)/);
  });
});

/* -------------------------------------------------------------------------
 * Severity in words.
 * ---------------------------------------------------------------------- */

describe('severity is carried in text as well as colour', () => {
  it('has a word behind every rule that paints a severity colour', () => {
    const ledger = new Set(SEVERITY_CARRIERS.map((c) => c.selector));
    const painted = tokenDeclarations(CSS)
      .filter((d) => SEVERITY_TOKENS.includes(d.tokens[0]))
      .filter((d) => d.property === 'color' || d.property === 'fill')
      .map((d) => d.selector);
    const unledgered = painted.filter((s) => !ledger.has(s));
    assert.deepEqual(
      unledgered,
      [],
      `these rules paint a severity colour with nothing recording what says it in words:\n  ${unledgered.join('\n  ')}`,
    );
  });

  it('has no stale ledger entries pointing at rules that are gone', () => {
    const painted = new Set(
      tokenDeclarations(CSS)
        .filter((d) => SEVERITY_TOKENS.includes(d.tokens[0]))
        .map((d) => d.selector),
    );
    const stale = SEVERITY_CARRIERS.filter((c) => !painted.has(c.selector)).map((c) => c.selector);
    assert.deepEqual(stale, [], `ledger entries with no rule:\n  ${stale.join('\n  ')}`);
  });

  it('actually produces those words', () => {
    // The ledger names functions; these are the functions.
    assert.equal(severityWord('error'), 'error');
    assert.equal(severityWord('warning'), 'warning');
    assert.equal(severityWord('hint'), 'hint');

    // The pill's VISIBLE text is the rule now (an owner decision, 2026-08-13),
    // so the ledger for `.pill-*` names `pillName` instead — the accessible
    // name, which a screen reader announces in place of the text. That is the
    // carrier, and this is what makes the swap legal under story 4.5 rather
    // than a quiet return to severity-by-colour: assert the severity word is in
    // it, for every severity, and assert the visible text is NOT the carrier by
    // showing it says something else entirely.
    for (const severity of ['error', 'warning', 'hint'] as const) {
      const finding = {
        rule: 'plaintext-credential',
        severity,
        title: 'Credential written in plain text',
        message: 'a literal credential sits in the file',
        subjects: ['services.db'],
        anchors: [
          { label: 'written here', path: 'services.db.environment.POSTGRES_PASSWORD', origin: { file: '/w/compose.yaml', line: 76, column: 26, step: 0 } },
        ],
      };
      assert.equal(pillText(finding), 'plaintext-credential');
      assert.match(pillName(finding), new RegExp(`\\b${severity}\\b`), 'the pill’s accessible name does not say the severity');
      assert.match(pillTitle(finding), new RegExp(`\\b${severity}\\b`), 'the pill’s tooltip does not say the severity');
      assert.notEqual(pillName(finding), pillText(finding), 'the accessible name adds nothing to the text');
    }
    // A finding the core sent with no rule falls back to the word rather than
    // rendering an empty pill — which passes every colour check there is.
    const nameless = {
      rule: '',
      severity: 'warning' as const,
      title: '',
      message: 'something',
      subjects: [],
      anchors: [{ label: '', path: 'services.db', origin: { file: '/w/compose.yaml', line: 1, column: 1, step: 0 } }],
    };
    assert.equal(pillText(nameless), 'warning');

    const badge = severityBadge({ error: 2, warning: 1, hint: 0 });
    assert.equal(badge?.word, '2 errors, 1 warning');
    // Was: `assert.ok(badge.word.length > 0)` immediately after the exact
    // equality above, which cannot fail — the equality already fixed the
    // string. What the check MEANT is that the badge does not carry its meaning
    // in the count and the colour alone, so that is what it now asserts.
    assert.match(badge!.word, /error|warning|hint/, 'the badge names no severity');
    const hintsOnly = severityBadge({ error: 0, warning: 0, hint: 3 });
    assert.match(hintsOnly!.word, /hint/, 'a hint-only badge says nothing but a number');
    assert.notEqual(hintsOnly!.word, String(hintsOnly!.count));

    const markers = markerIndex(
      {
        edges: [],
        dangling: [
          {
            kind: 'network',
            from: 'services.web',
            to: '',
            ref: 'nope',
            reason: 'no such network',
            origin: { file: 'compose.yaml', line: 1, column: 1, step: 0 },
          },
        ],
        cycles: [['services.a', 'services.b']],
      },
      ['services.api.build'],
    );
    assert.match(markers['services.web'][0], /^unresolved/);
    assert.match(markers['services.a'][0], /^dependency cycle/);
    assert.match(markers['services.api.build'][0], /^missing/);
  });
});

/* -------------------------------------------------------------------------
 * Token pairings.
 * ---------------------------------------------------------------------- */

describe('colour pairings are ones VS Code guarantees', () => {
  // NOT a measured ratio. This process cannot render, has no theme loaded and
  // cannot resolve a custom property to a colour; any number it printed would
  // be invented. What it can refuse is a foreground token painted on a
  // background token it was never contributed against, which is where a real
  // contrast failure in a themed extension comes from.
  it('knows where every ink token in the stylesheet lands', () => {
    const unledgered = inkTokens(CSS).filter((t) => INK_ON[t] === undefined);
    assert.deepEqual(
      unledgered,
      [],
      `these colours are painted with nothing recording which surface they land on:\n  ${unledgered.join('\n  ')}`,
    );
  });

  it('has no stale ink ledger entries', () => {
    const painted = new Set(inkTokens(CSS));
    const stale = Object.keys(INK_ON).filter((t) => !painted.has(t));
    assert.deepEqual(stale, [], `ink ledger entries no rule paints:\n  ${stale.join('\n  ')}`);
  });

  it('knows what is legible against every surface the stylesheet paints', () => {
    // Without this, a new background token could be introduced and the ink
    // already on that element would silently start rendering against it.
    const known = new Set([
      ...Object.keys(GUARANTEED_ON),
      ...Object.keys(OVERLAY_BACKGROUNDS),
      ...NON_TEXT_SURFACES,
    ]);
    const undeclared = surfaceTokens(CSS).filter((t) => !known.has(t));
    assert.deepEqual(
      undeclared,
      [],
      `these surfaces are painted with nothing recording what is legible on them:\n  ${undeclared.join('\n  ')}`,
    );
  });

  it('finds every declared pairing in the guarantee table', () => {
    const bad: string[] = [];
    for (const [ink, surfaces] of Object.entries(INK_ON)) {
      for (const surface of surfaces) {
        if (!isGuaranteedPairing(ink, surface)) {
          bad.push(`--vscode-${ink} on --vscode-${surface}`);
        }
      }
    }
    assert.deepEqual(
      bad,
      [],
      `VS Code does not contribute these foregrounds against these backgrounds:\n  ${bad.join('\n  ')}`,
    );
  });

  it('refuses a pairing the platform never promised', () => {
    assert.equal(isGuaranteedPairing('foreground', 'editor-background'), true);
    assert.equal(isGuaranteedPairing('foreground', 'input-background'), false);
    assert.equal(isGuaranteedPairing('descriptionForeground', 'button-background'), false);
    // Contrast is symmetric: the badge count is the editor background painted
    // over a disc filled with the error foreground.
    assert.equal(isGuaranteedPairing('editor-background', 'editorError-foreground'), true);
  });

  it('reads the guarantee table as a table and not as a wish', () => {
    for (const [background, inks] of Object.entries(GUARANTEED_ON)) {
      assert.ok(inks.length > 0, `${background} guarantees nothing and should not be listed`);
    }
  });

  it('classifies an SVG fill as ink unless the shape is declared', () => {
    // The safe direction to be wrong in: an unclassified `fill` is checked as
    // though it were text, so a new shape rule fails until someone says which
    // it is, rather than passing silently as decoration.
    const decls = inkDeclarations('.mystery-thing { fill: var(--vscode-charts-blue); }');
    assert.equal(decls.length, 1);
    assert.equal(decls[0].selector, '.mystery-thing');
    // Was: `assert.equal(INK_ON['charts-blue'], undefined)` — a statement about
    // the real ledger, which happens not to mention that token, and nothing at
    // all about the classification this test is named for.
    //
    // The property is the direction of the default: an unclassified `fill` is
    // treated as INK and therefore has to be declared, while a selector listed
    // as a shape is not.
    assert.deepEqual(inkTokens('.mystery-thing { fill: var(--vscode-charts-blue); }'), ['charts-blue']);
    const shape = SHAPE_FILL_SELECTORS[0];
    assert.deepEqual(inkTokens(`${shape} { fill: var(--vscode-charts-blue); }`), []);
  });

  /* -----------------------------------------------------------------------
   * The pairings the STYLESHEET states, rather than the ones a table claims.
   * -------------------------------------------------------------------- */

  // THE DEFECT THIS CLOSES. Every check above compares INK_ON against
  // GUARANTEED_ON — two hand-written tables — and never asks the stylesheet
  // which ink lands on which surface. A real mispairing planted in style.css:
  //
  //   .zz-pair { background: var(--vscode-button-background);
  //              color: var(--vscode-descriptionForeground); }
  //
  // passed 352 of 352, even though `isGuaranteedPairing('descriptionForeground',
  // 'button-background')` returns false in the test three screens up. The rule
  // is in the CSS, both ledgers already mention both tokens, and nothing joined
  // the two facts.
  describe('pairings derived from the stylesheet, not from a table beside it', () => {
    it('finds no unguaranteed pairing the stylesheet itself states', () => {
      const bad = derivedPairings(CSS)
        .filter((p) => !isGuaranteedPairing(p.ink, p.on))
        .filter((p) => !isExcepted(p));
      assert.deepEqual(
        bad,
        [],
        `the stylesheet paints these on surfaces VS Code does not contribute them against:\n${bad
          .map((p) => `  ${p.selector} — --vscode-${p.ink} on --vscode-${p.on} (${p.via})`)
          .join('\n')}`,
      );
    });

    it('catches the mispairing that passed 352 of 352', () => {
      const planted =
        `${CSS}\n.zz-pair { background: var(--vscode-button-background); ` +
        `color: var(--vscode-descriptionForeground); }\n`;
      const found = derivedPairings(planted).filter(
        (p) => p.selector === '.zz-pair' && !isGuaranteedPairing(p.ink, p.on) && !isExcepted(p),
      );
      assert.equal(found.length, 1, 'a mispairing written into the stylesheet is still invisible');
      assert.equal(found[0].via, 'same-rule');
    });

    it('reads a pairing out of an ancestor selector as well as out of one rule', () => {
      const css = '.pane { background: var(--vscode-button-background); }\n.pane .x { color: var(--vscode-descriptionForeground); }';
      assert.deepEqual(
        derivedPairings(css).map((p) => `${p.ink} on ${p.on} via ${p.via}`),
        ['descriptionForeground on button-background via ancestor'],
      );
    });

    it('does not invent a surface for a rule that clears its own background', () => {
      // `.field-value.is-unset` sets `background: transparent`, so its backdrop
      // is whatever contains it — NOT what `.field-value` alone declares.
      // Deriving one here would report a pairing that never happens.
      const css =
        '.v { background: var(--vscode-input-background); color: var(--vscode-input-foreground); }\n' +
        '.v.is-unset { background: transparent; color: var(--vscode-descriptionForeground); }';
      assert.deepEqual(
        derivedPairings(css).filter((p) => p.selector === '.v.is-unset'),
        [],
      );
    });

    it('keeps every exception attached to a rule that still exists', () => {
      // An exception is an argued claim about one rule. When the rule changes
      // or goes, the argument goes with it — otherwise this list becomes the
      // place mispairings are quietly parked.
      const derived = derivedPairings(CSS);
      for (const e of DERIVED_EXCEPTIONS) {
        assert.ok(
          derived.some((p) => p.selector === e.selector && p.ink === e.ink && p.on === e.on),
          `the exception for ${e.selector} (--vscode-${e.ink} on --vscode-${e.on}) matches no rule`,
        );
        assert.ok(e.why.length > 80, `the exception for ${e.selector} is not argued`);
      }
    });

    it('states exactly how much of the stylesheet it could NOT derive', () => {
      // The residue, counted rather than assumed empty. An ink token whose
      // surface comes from an ancestor the selector never names cannot be
      // resolved from CSS at all — that is what INK_ON is still for, so every
      // underived token must at least be in the ledger.
      const residue = underivedInk(CSS);
      const unledgered = residue.filter((t) => INK_ON[t] === undefined);
      assert.deepEqual(
        unledgered,
        [],
        `these are neither derivable from the stylesheet nor recorded in INK_ON:\n  ${unledgered.join('\n  ')}`,
      );
      // And the derivation must keep covering something: a `derivedPairings`
      // that returned [] would satisfy the first test in this block trivially.
      assert.ok(
        derivedPairings(CSS).length >= 12,
        `only ${derivedPairings(CSS).length} pairings were derived — the reader is checking nothing`,
      );
    });
  });

  /* -----------------------------------------------------------------------
   * A surface on a surface, which nothing above this line was about.
   *
   * THE DEFECT THIS CLOSES. `.field-value` painted `--vscode-input-background`
   * on a pane of `--vscode-editor-background`. In Light+ both are #FFFFFF, so
   * the value chip — the thing a reader clicks to change a value — was not
   * there. Every check in this file passed, because they all asked whether the
   * INK was legible, and `input-foreground` on `input-background` is a pairing
   * VS Code genuinely contributes. Nobody asked whether the surface existed.
   *
   * The same shape hid `.node-box`: `sideBar-background` on
   * `editor-background`, which in Dark High Contrast is #000000 on #000000.
   *
   * The theme values below are VS Code's own colour-registry defaults (MIT).
   * They live in the TEST rather than in `a11y.ts` because a hex in a shipped
   * source is a colour literal and the guard in `layout.test.ts` rejects it —
   * correctly: this is a description of the platform, not a palette.
   * -------------------------------------------------------------------- */
  describe('a raised surface separates from the pane it sits on', () => {
    const THEMES: Record<string, Record<string, string>> = {
      'Dark+': {
        'editor-background': '#1E1E1E',
        'editorWidget-background': '#252526',
        'editorWidget-border': '#454545',
        'sideBar-background': '#252526',
        'input-background': '#3C3C3C',
        'input-border': '#3C3C3C',
        'panel-border': '#80808059',
      },
      'Light+': {
        'editor-background': '#FFFFFF',
        'editorWidget-background': '#F3F3F3',
        'editorWidget-border': '#C8C8C8',
        'sideBar-background': '#F3F3F3',
        'input-background': '#FFFFFF',
        'input-border': '#CECECE',
        'panel-border': '#80808059',
      },
      // In the high-contrast themes every background is the same colour by
      // design and `contrastBorder` does the separating. `editorWidget.border`
      // is defined AS `contrastBorder` in both, which is why the chip and the
      // node box survive there.
      'Dark High Contrast': {
        'editor-background': '#000000',
        'editorWidget-background': '#0C141F',
        'editorWidget-border': '#6FC3DF',
        'sideBar-background': '#000000',
        'input-background': '#000000',
        'input-border': '#6FC3DF',
        'panel-border': '#6FC3DF',
        contrastBorder: '#6FC3DF',
      },
      'Light High Contrast': {
        'editor-background': '#FFFFFF',
        'editorWidget-background': '#FFFFFF',
        'editorWidget-border': '#0F4A85',
        'sideBar-background': '#FFFFFF',
        'input-background': '#FFFFFF',
        'input-border': '#0F4A85',
        'panel-border': '#0F4A85',
        contrastBorder: '#0F4A85',
      },
    };

    it('reads the surface and the edge out of the stylesheet, not out of a table', () => {
      // If this stops being true the check below is measuring nothing, so it
      // is asserted rather than assumed.
      for (const raised of RAISED_SURFACES) {
        const surface = paintChain(CSS, raised.selector, ['background', 'background-color', 'fill']);
        const edge = paintChain(CSS, raised.selector, raised.edge);
        assert.ok(surface.length > 0, `${raised.selector} declares no surface in style.css`);
        assert.ok(edge.length > 0, `${raised.selector} declares no ${raised.edge.join('/')}`);
      }
    });

    it('is told apart from its pane in every default theme', () => {
      const flat: string[] = [];
      for (const raised of RAISED_SURFACES) {
        for (const [name, theme] of Object.entries(THEMES)) {
          const s = separates(CSS, raised, theme);
          if (s.by === null) {
            flat.push(`${name}: ${raised.what} — ${s.why}`);
          }
        }
      }
      assert.deepEqual(
        flat,
        [],
        `these surfaces vanish into the surface behind them:\n  ${flat.join('\n  ')}`,
      );
    });

    it('catches the chip that shipped invisible in Light+', () => {
      // The rule EXACTLY as it stood before this was fixed. A check that cannot
      // reproduce the defect it was written for is a check that cannot fail.
      const before =
        '.field-value { color: var(--vscode-input-foreground); ' +
        'background: var(--vscode-input-background); ' +
        'border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); }';
      const chip = RAISED_SURFACES.find((r) => r.selector === '.field-value')!;
      assert.equal(separates(before, chip, THEMES['Dark+']).by, 'fill');
      const light = separates(before, chip, THEMES['Light+']);
      assert.equal(light.by, null, 'the white-on-white chip is reported as visible');
      assert.match(light.why, /#FFFFFF/);
    });

    it('does not accept an ordinary hairline as a substitute for a surface', () => {
      // The loophole that would have made this check pass on the defect it was
      // written for: the broken chip DID have a border, and that border DID
      // differ from the pane. A pale rule around nothing is not a chip, so only
      // `contrastBorder` — which the theme itself nominates for exactly this
      // job — is accepted in place of a fill.
      const chip = RAISED_SURFACES.find((r) => r.selector === '.field-value')!;
      const hairline =
        '.field-value { background: var(--vscode-input-background); ' +
        'border: 1px solid var(--vscode-input-border); }';
      assert.notEqual(
        THEMES['Light+']['input-border'],
        THEMES['Light+']['editor-background'],
        'the premise of this test is that the hairline IS a different colour',
      );
      assert.equal(separates(hairline, chip, THEMES['Light+']).by, null);
      const nominated =
        '.field-value { background: var(--vscode-input-background); ' +
        'border: 1px solid var(--vscode-editorWidget-border); }';
      assert.equal(
        separates(nominated, chip, THEMES['Light High Contrast']).by,
        'contrast border',
      );
    });

    it('records what it does NOT prove about the node box', () => {
      // Honesty about the second half of the fix. `.node-box` moved from
      // `sideBar-background` + `panel-border` to `editorWidget-background` +
      // `editorWidget-border`, and the OLD rule passes this check in all four
      // themes: the fills differ in the default themes, and in the
      // high-contrast ones `panel.border` is itself `contrastBorder`.
      //
      // What changed for the node box is the WEIGHT of the edge — a translucent
      // 35% grey became an opaque border colour — and no token comparison can
      // see that. The screenshot harness is the evidence for it, and this test
      // exists so nobody reads the check above as covering it.
      const box = RAISED_SURFACES.find((r) => r.selector === '.node-box')!;
      const before =
        '.node-box { fill: var(--vscode-sideBar-background); ' +
        'stroke: var(--vscode-panel-border); stroke-width: 1; }';
      for (const theme of Object.values(THEMES)) {
        assert.notEqual(separates(before, box, theme).by, null);
      }
    });
  });

  it('says out loud what it did not check', () => {
    assert.ok(MANUAL_CONTRAST_CHECKS.length >= 5);
    // TESTING.md is where a human looks; a list only this file knows is a list
    // nobody performs.
    const testing = read('TESTING.md');
    for (const line of MANUAL_CONTRAST_CHECKS) {
      assert.ok(
        testing.includes(line),
        `TESTING.md does not carry this manual check:\n  ${line}`,
      );
    }
  });
});
