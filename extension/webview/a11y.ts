// The accessibility floor, expressed as data a test can check — story 4.5.
//
// 4.1 and 4.2 built the floor opportunistically: a roving tabindex here, an
// aria-label there, a focus ring never removed. Nothing tracked it, and a floor
// with nothing tracking it erodes one feature at a time. Epics 5 and 6 then
// added an inspector, an unset-key list, provenance buttons, a pending-diff
// strip and a Dockerfile form — five new surfaces, none of them covered by a
// check.
//
// So this file is the ledger, and a11y.test.ts is the enforcement. Everything
// here is pure and takes source text, because the only thing that can be
// asserted about a webview under `node --test` is what its source says. Nothing
// here renders and nothing here claims to have rendered.
//
// THREE THINGS ARE CHECKED, and one is deliberately NOT.
//
//   1. Every interactive element is reachable and named. Scanned out of the
//      source rather than listed, so a control added tomorrow is checked too.
//   2. Severity is carried in words, never by colour alone. Every rule in the
//      stylesheet that paints a severity colour must be in a ledger naming the
//      function that supplies the word.
//   3. Every colour token pairing is one VS Code guarantees to be legible —
//      a foreground token on a background token it is contributed against.
//
// NOT CHECKED: an actual contrast ratio. This process cannot render a pixel,
// has no theme loaded, and cannot resolve a `--vscode-*` custom property to a
// colour. Any number it printed would be invented. What it CAN do is refuse a
// pairing VS Code never promised, which is where a real contrast failure comes
// from in a themed extension. MANUAL_CONTRAST_CHECKS below says exactly what a
// human must then check by eye, and TESTING.md repeats it.

/* -------------------------------------------------------------------------
 * 1. Interactive elements.
 * ---------------------------------------------------------------------- */

/** Tags the platform makes focusable and operable from the keyboard for free. */
export const NATIVELY_FOCUSABLE = ['button', 'input', 'select', 'textarea', 'a'];

/**
 * Tags that must carry an accessible name.
 *
 * A control a screen reader announces as "button" is a control nobody can use,
 * and it is exactly what an icon-only affordance produces.
 */
export const MUST_BE_NAMED = ['button', 'input', 'select', 'textarea'];

/** Events that make an element a capability rather than a decoration. */
export const POINTER_EVENTS = ['click', 'dblclick', 'pointerdown', 'mousedown', 'pointerup'];

/**
 * Receivers that are not elements of ours and carry no keyboard contract.
 *
 * `window` and `document` are the page itself; a listener on either is a global
 * hook, not a control a reader has to be able to reach.
 */
export const GLOBAL_RECEIVERS = ['window', 'document', 'body'];

export interface ElementBinding {
  /** The identifier, with any leading `this.` stripped. */
  name: string;
  tag: string;
  line: number;
}

/**
 * Every element the source creates with a literal tag.
 *
 * Covers `document.createElement('button')`, the `createElementNS` form the
 * graph uses for SVG, and the local `el('span', 'class')` helper every view
 * file defines. A tag that is a variable is skipped: there is nothing to check
 * against, and the only such call is the `el` helper's own body.
 */
/**
 * Source with its comments removed and its line numbers intact.
 *
 * This codebase explains itself at length, and the explanations quote the code
 * they are about — including the rule that there is no `outline: none` anywhere
 * in it. A scanner that reads comments reports the documentation as the defect.
 * A `//` preceded by a colon is left alone: that is a URL, and the SVG
 * namespace is one.
 */
export function stripComments(source: string): string {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
    .replace(/(^|[^:])\/\/[^\n]*/g, (_, lead: string) => lead);
}

export function elementBindings(source: string): ElementBinding[] {
  source = stripComments(source);
  const pattern =
    /(?:const|let|var|readonly)?\s*(?:this\.)?([A-Za-z_$][\w$]*)\s*(?::\s*[^=;\n]+)?=\s*(?:document\.createElement(?:NS)?\(\s*(?:SVG_NS\s*,\s*)?|el\(\s*)'([a-z]+)'/g;
  const out: ElementBinding[] = [];
  for (const m of source.matchAll(pattern)) {
    out.push({ name: m[1], tag: m[2], line: lineOf(source, m.index ?? 0) });
  }
  return out;
}

export interface HandlerSite {
  name: string;
  event: string;
  line: number;
}

/** Every `x.addEventListener('event', …)` in the source, by receiver. */
export function handlerSites(source: string): HandlerSite[] {
  source = stripComments(source);
  const pattern = /(?:this\.)?([A-Za-z_$][\w$]*)\.addEventListener\(\s*'([a-z]+)'/g;
  const out: HandlerSite[] = [];
  for (const m of source.matchAll(pattern)) {
    out.push({ name: m[1], event: m[2], line: lineOf(source, m.index ?? 0) });
  }
  return out;
}

/**
 * Whether an element is given an accessible name anywhere in the file.
 *
 * `textContent`, `aria-label` and `aria-labelledby` are the direct forms. An
 * element that is only appended to is followed one level down: the narrow
 * list's rows are buttons built from two named spans, and that IS a name — but
 * a button holding a single unnamed decoration is not, and that is the case
 * this has to keep failing.
 */
export function hasAccessibleName(source: string, name: string, depth = 2): boolean {
  const direct = new RegExp(
    `(?:this\\.)?${escapeIdent(name)}\\.(?:textContent\\s*=|setAttribute\\(\\s*'aria-label(?:ledby)?')`,
  );
  if (direct.test(source)) {
    return true;
  }
  if (depth <= 0) {
    return false;
  }
  for (const child of appendedIdentifiers(source, name)) {
    if (hasAccessibleName(source, child, depth - 1)) {
      return true;
    }
  }
  return false;
}

/** The identifiers a given element is passed as children of `append`. */
export function appendedIdentifiers(source: string, name: string): string[] {
  const pattern = new RegExp(
    `(?:this\\.)?${escapeIdent(name)}\\.append(?:Child)?\\(([^)]*)\\)`,
    'g',
  );
  const out = new Set<string>();
  for (const m of source.matchAll(pattern)) {
    for (const raw of m[1].split(',')) {
      const arg = raw.trim().replace(/^this\./, '');
      if (/^[A-Za-z_$][\w$]*$/.test(arg)) {
        out.add(arg);
      }
    }
  }
  return [...out];
}

/**
 * Whether an element is reachable from the keyboard: a native control, or one
 * given a tabindex explicitly.
 *
 * A roving `tabindex="-1"` counts — that is the listbox pattern the graph uses,
 * where the container holds the tab stop and arrow keys move within it. What
 * does not count is nothing at all.
 */
export function isKeyboardReachable(source: string, binding: ElementBinding): boolean {
  if (NATIVELY_FOCUSABLE.includes(binding.tag)) {
    return true;
  }
  const ident = escapeIdent(binding.name);
  return new RegExp(
    `(?:this\\.)?${ident}\\.(?:tabIndex\\s*=|setAttribute\\(\\s*'tabindex')`,
  ).test(source);
}

export interface A11yDefect {
  file: string;
  line: number;
  element: string;
  problem: string;
}

/**
 * Every interactive element in one source file that a keyboard reader could not
 * use, or a screen reader could not name.
 *
 * This is the check that has to keep working on code nobody has written yet, so
 * it scans rather than enumerates: a control added next year is scanned by the
 * same pass that scans today's.
 */
export function auditSource(file: string, rawSource: string): A11yDefect[] {
  const source = stripComments(rawSource);
  const bindings = elementBindings(source);
  const byName = new Map<string, ElementBinding>();
  for (const b of bindings) {
    // Last binding of a name wins; the files here bind each name once, and a
    // shadowed name is checked against the shape it most recently had.
    byName.set(b.name, b);
  }
  const defects: A11yDefect[] = [];

  for (const b of bindings) {
    if (MUST_BE_NAMED.includes(b.tag) && !hasAccessibleName(source, b.name)) {
      defects.push({
        file,
        line: b.line,
        element: `<${b.tag}> ${b.name}`,
        problem: 'has no accessible name — no textContent, aria-label or named child',
      });
    }
  }

  for (const site of handlerSites(source)) {
    if (!POINTER_EVENTS.includes(site.event) || GLOBAL_RECEIVERS.includes(site.name)) {
      continue;
    }
    const binding = byName.get(site.name);
    if (!binding) {
      defects.push({
        file,
        line: site.line,
        element: site.name,
        problem: `takes a '${site.event}' handler but this file never creates it — the check cannot tell whether it is reachable`,
      });
      continue;
    }
    if (!isKeyboardReachable(source, binding)) {
      defects.push({
        file,
        line: site.line,
        element: `<${binding.tag}> ${binding.name}`,
        problem: `takes a '${site.event}' handler and is not focusable — that capability is pointer-only`,
      });
    }
  }

  return defects;
}

/* -------------------------------------------------------------------------
 * 2. Severity in words.
 * ---------------------------------------------------------------------- */

/** The tokens that mean "this is a problem". Colour, and never only colour. */
export const SEVERITY_TOKENS = [
  'editorError-foreground',
  'editorWarning-foreground',
  'editorInfo-foreground',
];

/**
 * Every stylesheet rule that paints a severity colour, and what supplies the
 * WORD that must accompany it.
 *
 * A rule painting one of these tokens and missing from this ledger fails the
 * test — which is what stops the next feature from shipping a red dot with no
 * text behind it.
 */
export const SEVERITY_CARRIERS: { selector: string; words: string }[] = [
  { selector: '.marker-unresolved, .marker-cycle', words: 'layout.markerIndex — the marker text begins `unresolved` or `dependency cycle`' },
  { selector: '.marker-missing', words: 'layout.markerIndex — the marker text begins `missing`' },
  { selector: '.service-row.is-missing .service-detail', words: 'main.renderList — the row detail ends `· missing — this file is not on disk`' },
  { selector: '.field-value.is-unresolved, .resolution.is-unresolved', words: 'inspector.renderResolution — the line names each undefined variable' },
  { selector: '.resolution-note', words: 'inspector.renderResolution — `… undefined`' },
  { selector: '.prov-word.is-many', words: 'inspector.provenanceOf — `overrides` and the file that won' },
  { selector: '.diag-error .pill, .diag-error .diag-text', words: 'inspector.pillName — the pill’s accessible name and title begin `error`, and .diag-text is the finding’s own sentence' },
  { selector: '.diag-warning .pill, .diag-warning .diag-text', words: 'inspector.pillName — the pill’s accessible name and title begin `warning`, and .diag-text is the finding’s own sentence' },
  { selector: '.diag-hint .pill, .diag-hint .diag-text', words: 'inspector.pillName — the pill’s accessible name and title begin `hint`, and .diag-text is the finding’s own sentence' },
  { selector: '.node-badge-error', words: 'layout.severityBadge — `word` is in the node’s aria-label and its tooltip' },
  { selector: '.node-badge-warning', words: 'layout.severityBadge — `word` is in the node’s aria-label and its tooltip' },
  { selector: '.node-badge-hint', words: 'layout.severityBadge — `word` is in the node’s aria-label and its tooltip' },
  { selector: '.field-note.is-staged-note', words: 'inspector.renderField — `staged — the file still says …`' },
  // Decision 21. The ink says WHICH of the two kinds of note this is; the words
  // say everything else, and they come from the core — `Availability.detail`,
  // one sentence per reason, naming the anchor and the line.
  { selector: '.field-note.is-override', words: 'inspector.appendAvailabilityNote — the core’s sentence: `… arrives through `<<: *name` on line N. Writing a value adds … which overrides …`' },
  // The pill moved inline with the field it concerns (DESIGN.md:180), so it
  // carries the severity colour on its own selector rather than inheriting from
  // a `.diag-*` wrapper.
  //
  // Its VISIBLE text is now the rule (`plaintext-credential`), an owner
  // decision: the severity word was the same on every pill in the pane and told
  // the reader nothing the colour had not already said, while the rule is the
  // one thing a pill can say that nothing else on the row does. The severity did
  // not become colour-only — `inspector.pillName` puts the word in the pill's
  // accessible name, which a screen reader announces INSTEAD of the visible
  // text, and `inspector.pillTitle` puts it in the tooltip. Text, twice, plus
  // the colour. Story 4.5's floor is what makes that swap legal rather than a
  // regression, so the carrier named here is `pillName` and not the rendering.
  { selector: '.pill-error', words: 'inspector.pillName — the pill’s accessible name and title begin `error`' },
  { selector: '.pill-warning', words: 'inspector.pillName — the pill’s accessible name and title begin `warning`' },
  { selector: '.pill-hint', words: 'inspector.pillName — the pill’s accessible name and title begin `hint`' },
  // Epic 8's upgrade pill. It takes the WARNING ink because an old base image
  // is a thing to attend to, and the words are on its face — the pill's visible
  // text is the core's own `node:22-alpine · minor · 40MB smaller`, and its
  // accessible name says all of that again plus what pressing it does. The
  // colour repeats what is written; it never carries it alone.
  { selector: '.pill-upgrade', words: 'imagepill.upgradePill — the pill’s visible text is hub.Report.Pill (`reference · kind · size`) and imagepill.pillName says the same in its accessible name' },
];

/**
 * Which files in `webview/` are shipped, given the directory listing.
 *
 * Story 4.5's suite claimed to scan "the shipped webview sources" and in fact
 * read a hardcoded array of seven filenames. Everything written after it was
 * unscanned by construction: a new file with an unnamed button and a
 * pointer-only div passed the accessibility floor clean, which is precisely the
 * failure the story existed to prevent — a check that cannot fail.
 *
 * A pure function over the listing, so the filter can be tested with names that
 * do not exist on disk. Tests are excluded because they are not shipped, and
 * because this suite has to be able to name the patterns it forbids.
 */
export function shippedSources(names: string[]): string[] {
  return names
    .filter((n) => n.endsWith('.ts') && !n.endsWith('.test.ts') && !n.endsWith('.d.ts'))
    .sort();
}

/* -------------------------------------------------------------------------
 * 3. Token pairings.
 * ---------------------------------------------------------------------- */

/**
 * `fill` is a surface here, not ink.
 *
 * SVG's `fill` paints text AND shapes, so the property alone does not say which
 * one a rule means. These selectors are the shapes; everything else that sets
 * `fill` is text and is checked as ink. A new shape rule not listed here is
 * treated as ink and fails until it is classified — which is the safe
 * direction to be wrong in.
 */
export const SHAPE_FILL_SELECTORS = [
  '.node-box',
  '.node-network .node-box, .node-volume .node-box, .node-config .node-box, .node-secret .node-box',
  '.node-dockerfile .node-box',
  '.edge-label-plate',
  '.node-badge-error',
  '.node-badge-warning',
  '.node-badge-hint',
];

/**
 * `fill` on a non-text MARK: an arrowhead.
 *
 * Neither ink nor a surface — nothing is read against it and nothing is read on
 * it. It is a graphical object, which WCAG holds to 3:1 rather than 4.5:1, and
 * that is a judgement about a rendered pixel this process cannot make. The
 * arrowheads are in MANUAL_CONTRAST_CHECKS by way of the node-kind line.
 */
export const MARK_FILL_SELECTORS = ['.arrow-head', '.arrow-strong', '.arrow-build'];

/**
 * Backgrounds that are translucent overlays rather than opaque surfaces.
 *
 * VS Code paints these over the editor background, so the effective backdrop of
 * text on them is the editor background — which is what the pairing check
 * resolves them to. This is the one place the check reasons rather than looks
 * up, so it is also the first line of MANUAL_CONTRAST_CHECKS.
 */
export const OVERLAY_BACKGROUNDS: Record<string, string> = {
  'diffEditor-insertedTextBackground': 'editor-background',
  'diffEditor-removedTextBackground': 'editor-background',
  'list-hoverBackground': 'editor-background',
};

/**
 * Which foreground tokens VS Code contributes against which background tokens.
 *
 * This is the whole basis of the contrast check, so every entry is a pairing VS
 * Code's own UI makes — not a pairing that looks fine in the theme this was
 * written against. Adding an entry is a claim about the platform and should be
 * argued for in the comment beside it.
 */
export const GUARANTEED_ON: Record<string, string[]> = {
  // The editor surface. The workbench renders body text, description text,
  // links, the three diagnostic colours and the chart colours over it.
  'editor-background': [
    'foreground',
    'editor-foreground',
    'descriptionForeground',
    'disabledForeground',
    'editorError-foreground',
    'editorWarning-foreground',
    'editorInfo-foreground',
    'textLink-foreground',
    'textLink-activeForeground',
    'charts-orange',
    'gitDecoration-addedResourceForeground',
    'gitDecoration-deletedResourceForeground',
  ],
  // The side bar. VS Code's own explorer draws `foreground` and
  // `descriptionForeground` on it, and the git decorations too.
  'sideBar-background': [
    'foreground',
    'descriptionForeground',
    'editorError-foreground',
    'editorWarning-foreground',
    'editorInfo-foreground',
    'charts-orange',
    'gitDecoration-addedResourceForeground',
    'gitDecoration-deletedResourceForeground',
  ],
  // A widget surface: hovers, the find widget, the suggest list.
  'editorWidget-background': [
    'editorWidget-foreground',
    'foreground',
    'editor-foreground',
    'descriptionForeground',
    'editorError-foreground',
    'editorWarning-foreground',
    'editorInfo-foreground',
  ],
  // A control surface. VS Code contributes exactly one foreground for it, plus
  // the placeholder colour; body text is NOT contributed against it.
  'input-background': ['input-foreground', 'input-placeholderForeground'],
  'button-background': ['button-foreground'],
  'button-hoverBackground': ['button-foreground'],
  'button-secondaryBackground': ['button-secondaryForeground'],
  'button-secondaryHoverBackground': ['button-secondaryForeground'],
  'list-activeSelectionBackground': ['list-activeSelectionForeground'],
  'list-hoverBackground': ['list-hoverForeground', 'foreground', 'descriptionForeground'],
  // The validation surfaces contribute their own foreground and nothing else.
  'inputValidation-errorBackground': ['inputValidation-errorForeground'],
};

/**
 * Surfaces that never hold text, so nothing has to be legible against them.
 *
 * Each one is a bar, a rule or a disc. The badge discs are the interesting
 * case: they are painted in a SEVERITY foreground and the count on them is
 * painted in the editor background, which is the same pairing as the ordinary
 * one read backwards — contrast is symmetric.
 */
export const NON_TEXT_SURFACES = [
  'focusBorder',
  'panel-border',
  'progressBar-background',
  'editorError-foreground',
  'editorWarning-foreground',
  'editorInfo-foreground',
];

export interface Pairing {
  ink: string;
  on: string;
}

/**
 * Whether a pairing is one VS Code guarantees.
 *
 * Contrast is symmetric, so a background token used as ink over a foreground
 * token used as a fill is the same ratio read the other way round — which is
 * what the diagnostic badge does: the count is painted in the editor background
 * over a disc filled with the error foreground.
 */
export function isGuaranteedPairing(ink: string, background: string): boolean {
  const bg = OVERLAY_BACKGROUNDS[background] ?? background;
  const fg = OVERLAY_BACKGROUNDS[ink] ?? ink;
  if ((GUARANTEED_ON[bg] ?? []).includes(fg)) {
    return true;
  }
  // The reversed reading: ink and surface swapped.
  return (GUARANTEED_ON[fg] ?? []).includes(bg);
}

/**
 * Every ink token the webview paints, and the surfaces it is painted on.
 *
 * The surface cannot be derived from CSS — it is decided by which element
 * contains which — so it is declared here and the test checks two things: that
 * every ink token in the stylesheet appears as a key (so a new colour cannot be
 * introduced without saying where it lands), and that every pairing listed is
 * one VS Code guarantees.
 */
export const INK_ON: Record<string, string[]> = {
  // Body text, node labels, the selected row. The service node box is filled
  // with the side bar surface; resource and Dockerfile boxes with the editor's.
  foreground: [
    'editor-background',
    'sideBar-background',
    'list-hoverBackground',
    // The value chip and the node box: both are the raised widget surface, so
    // the reader can see a chip is a chip in a theme where the input surface
    // and the editor surface are the same white.
    'editorWidget-background',
  ],
  'editor-foreground': ['editor-background', 'editorWidget-background', 'diffEditor-insertedTextBackground', 'diffEditor-removedTextBackground'],
  descriptionForeground: ['editor-background', 'sideBar-background', 'editorWidget-background'],
  // Disabled text — an instruction the engine will not rewrite. Exempt from AA
  // by WCAG 1.4.3 itself, and listed so the exemption is deliberate.
  disabledForeground: ['editor-background'],
  'editorError-foreground': ['editor-background', 'sideBar-background'],
  'editorWarning-foreground': ['editor-background', 'sideBar-background'],
  'editorInfo-foreground': ['editor-background', 'sideBar-background'],
  'textLink-foreground': ['editor-background'],
  'textLink-activeForeground': ['editor-background'],
  // The one identity colour: the Dockerfile node, on the editor surface.
  'charts-orange': ['editor-background'],
  'button-foreground': ['button-background', 'button-hoverBackground'],
  'button-secondaryForeground': ['button-secondaryBackground', 'button-secondaryHoverBackground'],
  'input-foreground': ['input-background'],
  'list-activeSelectionForeground': ['list-activeSelectionBackground'],
  'inputValidation-errorForeground': ['inputValidation-errorBackground'],
  'gitDecoration-addedResourceForeground': ['diffEditor-insertedTextBackground'],
  'gitDecoration-deletedResourceForeground': ['diffEditor-removedTextBackground'],
  // Used as INK over a severity fill: the badge count is the editor background
  // painted on the disc. Symmetric with the pairing above it.
  'editor-background': [
    'editorError-foreground',
    'editorWarning-foreground',
    'editorInfo-foreground',
  ],
};

/**
 * What a human must check by eye, because this process cannot.
 *
 * Printed by the test run so it is not a file nobody opens. Every line is a
 * thing the pairing check above deliberately does not prove.
 */
export const MANUAL_CONTRAST_CHECKS = [
  'Open the panel in Default Light Modern, Default Dark Modern, Light High Contrast and Dark High Contrast.',
  'The three diff line colours sit on a TRANSLUCENT diff background; the check resolves that to the editor background. Confirm added and removed lines are still readable in each theme, and in a theme with a heavy diff tint.',
  'The focus ring (--vscode-focusBorder) is a non-text indicator and needs 3:1 against whatever it surrounds. Tab to a node, a provenance link, an unset key, the divider, the search field, the focus and collapse buttons, an inspector field and the Save and Discard buttons, and confirm the ring is visible on each in all four themes.',
  'The selection ring and the focus ring are the same token. Confirm a selected-but-unfocused node is still distinguishable from a focused one.',
  'Node kinds are told apart by shape and dash pattern, not colour. Confirm that holds in Dark High Contrast, where dashes can vanish into the border colour.',
  'The dimmed state used by search and focus mode is an opacity, not a colour. Confirm dimmed nodes are still legible enough to read, and that matched nodes are obviously the ones in focus.',
  'The narrow (<600px) layout replaces the canvas with a list. Confirm the search field, its status line and the collapse controls are all still reachable and legible there.',
  'The value chip and the node box are the raised widget surface, separated from the pane by fill in the default themes and by --vscode-editorWidget-border in the high-contrast ones. RAISED_SURFACES checks that against VS Code\'s own defaults, which is four themes out of every theme there is. In a USER-authored theme that sets editorWidget.background to the editor background and contributes no widget border, the chip and the node box go flat again — confirm by eye in whatever theme you actually use.',
  'Most of the stylesheet pairs ink with a surface declared on an ANCESTOR the selector never names, which no CSS reader can resolve. derivedPairings() checks the ones it can and underivedInk() names the rest; those tokens rely on the INK_ON ledger, so read the ledger against the rendered pane once per release rather than trusting it.',
];

/* -------------------------------------------------------------------------
 * Stylesheet parsing. Pure, so the ledgers above can be checked against the
 * stylesheet as it actually stands rather than as it stood when it was read.
 * ---------------------------------------------------------------------- */

export interface CssDeclaration {
  selector: string;
  property: string;
  tokens: string[];
}

/** Every declaration in a stylesheet that references a `--vscode-*` token. */
export function tokenDeclarations(css: string): CssDeclaration[] {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '');
  const out: CssDeclaration[] = [];
  for (const rule of stripped.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
    const selector = rule[1].trim().replace(/\s+/g, ' ');
    for (const decl of rule[2].split(';')) {
      const m = decl.match(/^\s*([a-z-]+)\s*:\s*([\s\S]+)$/);
      if (!m) {
        continue;
      }
      const tokens = [...m[2].matchAll(/--vscode-([A-Za-z0-9-]+)/g)].map((t) => t[1]);
      if (tokens.length > 0) {
        out.push({ selector, property: m[1], tokens });
      }
    }
  }
  return out;
}

/** The ink declarations: `color` anywhere, and `fill` on anything not a shape. */
export function inkDeclarations(css: string): CssDeclaration[] {
  return tokenDeclarations(css).filter(
    (d) =>
      d.property === 'color' ||
      (d.property === 'fill' &&
        !SHAPE_FILL_SELECTORS.includes(d.selector) &&
        !MARK_FILL_SELECTORS.includes(d.selector)),
  );
}

/**
 * Every distinct ink token the stylesheet paints.
 *
 * Only the FIRST token of a `var(a, var(b))` chain: that is what renders in
 * every theme that defines it, which is the case the pairing table is about.
 * The fallback is what a theme that omits `a` falls back to, and checking it
 * as though it were the ink would forbid writing a fallback at all. Fallbacks
 * are in MANUAL_CONTRAST_CHECKS instead.
 */
export function inkTokens(css: string): string[] {
  const out = new Set<string>();
  for (const d of inkDeclarations(css)) {
    out.add(d.tokens[0]);
  }
  return [...out].sort();
}

/**
 * Every distinct token the stylesheet paints a SURFACE with.
 *
 * `background`, `background-color`, and `fill` on one of the declared shapes.
 * A surface nobody has said what is legible against is the other half of the
 * pairing check: without this, a new background token could be introduced and
 * the existing ink would silently start rendering on it.
 */
export function surfaceTokens(css: string): string[] {
  const out = new Set<string>();
  for (const d of tokenDeclarations(css)) {
    const isSurface =
      d.property === 'background' ||
      d.property === 'background-color' ||
      (d.property === 'fill' && SHAPE_FILL_SELECTORS.includes(d.selector));
    if (isSurface) {
      out.add(d.tokens[0]);
    }
  }
  return [...out].sort();
}

/* -------------------------------------------------------------------------
 * 3b. Pairings DERIVED from the stylesheet.
 *
 * INK_ON above is a hand-written table, and a hand-written table is a claim
 * about the stylesheet rather than a reading of it. The gap was measured: a real
 * mispairing planted in style.css —
 *
 *     .zz-pair { background: var(--vscode-button-background);
 *                color: var(--vscode-descriptionForeground); }
 *
 * — passed 352 of 352 tests, even though `isGuaranteedPairing` returns false for
 * that exact pair on the very next screen. Nothing asked the STYLESHEET which
 * ink lands on which surface; the check compared one table against another.
 *
 * So these functions read the pairings out of the CSS. Two shapes are derivable
 * with certainty and one is not:
 *
 *   1. SAME BLOCK. A rule that sets both a background and an ink is a pairing
 *      stated outright — the ink is painted on that background, always, and no
 *      containment reasoning is involved. This is the shape of the planted
 *      defect and of most of the real rules here.
 *   2. SELECTOR ANCESTRY. `.pending .diff-add` is painted on whatever surface
 *      `.pending` (or `.pending-diff`, or any prefix of the selector) declares.
 *      Where a prefix declares one, the pairing is derived.
 *   3. NOT DERIVABLE: an element whose background comes from an ancestor that is
 *      not a textual prefix of its own selector — `.field-value` inside
 *      `.inspector`, say, where the two selectors never appear together in one
 *      rule. CSS alone cannot say which elements contain which. Those are what
 *      INK_ON is still for, and `undeclaredInk` names them so the residue is
 *      counted rather than assumed to be empty.
 * ---------------------------------------------------------------------- */

export interface RuleBlock {
  selector: string;
  declarations: { property: string; value: string; tokens: string[] }[];
}

/** Every rule in a stylesheet, with its declarations kept together. */
export function ruleBlocks(css: string): RuleBlock[] {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '');
  const out: RuleBlock[] = [];
  for (const rule of stripped.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
    const selector = rule[1].trim().replace(/\s+/g, ' ');
    const declarations: RuleBlock['declarations'] = [];
    for (const decl of rule[2].split(';')) {
      const m = decl.match(/^\s*([a-z-]+)\s*:\s*([\s\S]+)$/);
      if (!m) {
        continue;
      }
      declarations.push({
        property: m[1],
        value: m[2].trim(),
        tokens: [...m[2].matchAll(/--vscode-([A-Za-z0-9-]+)/g)].map((t) => t[1]),
      });
    }
    if (declarations.length > 0) {
      out.push({ selector, declarations });
    }
  }
  return out;
}

/** Whether a declaration paints ink, given the selector it is written under. */
export function isInkProperty(selector: string, property: string): boolean {
  return (
    property === 'color' ||
    (property === 'fill' &&
      !SHAPE_FILL_SELECTORS.includes(selector) &&
      !MARK_FILL_SELECTORS.includes(selector))
  );
}

/** Whether a declaration paints a surface, given the selector it is under. */
export function isSurfaceProperty(selector: string, property: string): boolean {
  return (
    property === 'background' ||
    property === 'background-color' ||
    (property === 'fill' && SHAPE_FILL_SELECTORS.includes(selector))
  );
}

/**
 * A surface value that paints nothing: `transparent`, `none`, `inherit`.
 *
 * A rule that clears its background is not declaring a surface — whatever is
 * behind it still shows through — so it must not be read as one, or a
 * `background: none` on a link button would be reported as a surface the ink
 * lands on.
 */
function paintsNothing(value: string): boolean {
  return /^(transparent|none|inherit|initial|unset)\b/.test(value.trim());
}

/** Selector with pseudo-classes and pseudo-elements removed. */
function bare(part: string): string {
  return part.replace(/::?[a-z-]+(\([^)]*\))?/g, '');
}

/**
 * Every simple selector that declares a surface, and which token it declares.
 *
 * Keyed by the last compound of the selector and by each class within it, so
 * `.pending .pending-diff` is found under `.pending-diff` and `.button.is-x`
 * under `.button`. Last rule wins, matching the cascade for equal specificity.
 */
export function surfaceIndex(css: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const block of ruleBlocks(css)) {
    for (const decl of block.declarations) {
      if (!isSurfaceProperty(block.selector, decl.property) || decl.tokens.length === 0) {
        continue;
      }
      if (paintsNothing(decl.value)) {
        continue;
      }
      for (const alternative of block.selector.split(',')) {
        const parts = bare(alternative).trim().split(/\s+/).filter((p) => p !== '');
        const last = parts[parts.length - 1];
        if (last === undefined || last === '') {
          continue;
        }
        out.set(last, decl.tokens[0]);
        for (const cls of last.match(/\.[\w-]+/g) ?? []) {
          if (!out.has(cls) || cls === last) {
            out.set(cls, decl.tokens[0]);
          }
        }
      }
    }
  }
  return out;
}

export interface DerivedPairing extends Pairing {
  selector: string;
  /** How the surface was established: the same rule, or an ancestor selector. */
  via: 'same-rule' | 'ancestor';
}

/**
 * Every ink/surface pairing the STYLESHEET states or implies.
 *
 * This is the check that reads the CSS rather than a table beside it.
 */
export function derivedPairings(css: string): DerivedPairing[] {
  const surfaces = surfaceIndex(css);
  const out: DerivedPairing[] = [];
  for (const block of ruleBlocks(css)) {
    const ownSurface = block.declarations.find(
      (d) => isSurfaceProperty(block.selector, d.property) && d.tokens.length > 0 && !paintsNothing(d.value),
    );
    // A rule that CLEARS its background — `.field-value.is-unset` — is not
    // painted on whatever `.field-value` alone declares: it is painted on
    // whatever contains it, which is shape 3 and not derivable. Without this,
    // the derivation reports a pairing that does not happen.
    const clearsOwnSurface = block.declarations.some(
      (d) => isSurfaceProperty(block.selector, d.property) && paintsNothing(d.value),
    );
    for (const decl of block.declarations) {
      if (!isInkProperty(block.selector, decl.property) || decl.tokens.length === 0) {
        continue;
      }
      const ink = decl.tokens[0];
      if (ownSurface) {
        out.push({ ink, on: ownSurface.tokens[0], selector: block.selector, via: 'same-rule' });
        continue;
      }
      for (const alternative of block.selector.split(',')) {
        const parts = bare(alternative).trim().split(/\s+/).filter((p) => p !== '');
        // Nearest first: the innermost ancestor that declares a surface is the
        // one the text is actually painted over.
        for (let i = clearsOwnSurface ? parts.length - 2 : parts.length - 1; i >= 0; i--) {
          const found =
            surfaces.get(parts[i]) ??
            (parts[i].match(/\.[\w-]+/g) ?? []).map((c) => surfaces.get(c)).find((s) => s !== undefined);
          // The element's own compound only counts as its backdrop when the
          // surface was declared by a DIFFERENT rule; the same-rule case is
          // handled above and a rule that sets only ink inherits from above.
          if (found !== undefined && !(i === parts.length - 1 && parts.length === 1 && found === ink)) {
            out.push({ ink, on: found, selector: block.selector, via: 'ancestor' });
            break;
          }
        }
      }
    }
  }
  return out;
}

/**
 * Derived pairings VS Code does not contribute, kept deliberately.
 *
 * One, and it was found BY the derivation the moment it was written — which is
 * the point of deriving rather than tabulating. It is matched exactly, on all
 * three of selector, ink and surface, so this cannot be widened by accident and
 * a NEW mispairing still fails. `a11y.test.ts` also refuses a stale entry, so
 * an exception that no longer describes a rule has to be removed.
 *
 * There were two. The second was the warning ink on an unresolved value, which
 * landed on `input-background` — a surface VS Code contributes no severity
 * colour against. Moving the value chip to the raised widget surface to fix the
 * Light+ erasure closed that one for real: `editorWarning-foreground` on
 * `editorWidget-background` IS contributed, so the exception was deleted rather
 * than re-argued. An exception that stops being needed is the only good way for
 * one to leave this list.
 */
export const DERIVED_EXCEPTIONS: { selector: string; ink: string; on: string; why: string }[] = [
  {
    selector: '.field-block.is-readonly .field-value',
    ink: 'disabledForeground',
    on: 'editorWidget-background',
    why:
      'Disabled text — an instruction the engine will not rewrite. WCAG 1.4.3 exempts ' +
      'disabled controls from the contrast minimum, and INK_ON records the same exemption ' +
      'for the same token against the editor surface.',
  },
];

/** Whether a derived pairing is one of the argued exceptions. */
export function isExcepted(p: DerivedPairing): boolean {
  return DERIVED_EXCEPTIONS.some(
    (e) => e.selector === p.selector && e.ink === p.ink && e.on === p.on,
  );
}

/* -------------------------------------------------------------------------
 * 3c. Does a surface SEPARATE from the surface behind it?
 *
 * Everything above this line is about INK on a surface. Nothing was about one
 * SURFACE on another, and that is the hole the Light+ defect went through: the
 * value chip painted `input-background` on a pane of `editor-background`, and
 * in Light+ those two tokens are the same white, so the chip disappeared. Every
 * ink check passed, because `input-foreground` on `input-background` is a
 * pairing VS Code genuinely contributes — the ink was legible on a surface that
 * was not there.
 *
 * The same pairing put `sideBar-background` on `editor-background` for the node
 * box, and in Dark High Contrast those two are the same black.
 *
 * So: a raised surface must be TOLD APART from the surface it sits on. VS Code
 * gives two ways and the platform's own contract uses both — a different fill in
 * the default themes, and `contrastBorder` in the high-contrast ones, where
 * every surface is the same colour by design and the border does all the work.
 * ---------------------------------------------------------------------- */

/**
 * A raised surface in the stylesheet, and the surface it is drawn on.
 *
 * `selector` is looked up IN THE CSS rather than recorded here, so this is a
 * list of what must separate, not a second copy of what the tokens are — change
 * `.field-value`'s background and the check follows it.
 */
export interface RaisedSurface {
  /** What the reader sees, in words. */
  what: string;
  /** The selector whose background (or shape `fill`) is the raised surface. */
  selector: string;
  /** Properties that draw its edge, in the order the stylesheet may use them. */
  edge: string[];
  /** The token of the surface behind it. */
  on: string;
}

/**
 * The surfaces whose whole job is to be visible as a distinct thing.
 *
 * Two, and both were invisible in a real default theme when this was written.
 *
 * A third used to be recorded here as live residue: `.node-port .node-box`,
 * drawn on the INPUT surface because a published port names something outside
 * the stack. It measured 1.000:1 against the canvas in Light+ — white on white
 * — and the residue is now closed rather than recorded. The port box sets no
 * fill of its own, so it inherits `.node-box`'s, and the entry below covers it.
 * Its label moved off `input-foreground` in the same edit, because that token
 * is guaranteed against `input-background` and nothing else.
 */
export const RAISED_SURFACES: RaisedSurface[] = [
  {
    what: 'the value chip',
    selector: '.field-value',
    edge: ['border', 'border-color'],
    on: 'editor-background',
  },
  {
    // The one that matters, and the one a check on `.field-value` alone misses:
    // every CHANGEABLE value is an `<input>` carrying both classes, and this
    // rule wins the cascade.
    what: 'the editable value chip — the thing a reader clicks to change a value',
    selector: '.field-input',
    edge: ['border', 'border-color'],
    on: 'editor-background',
  },
  {
    what: 'the node box — every service, network and volume on the canvas',
    selector: '.node-box',
    edge: ['stroke'],
    on: 'editor-background',
  },
  {
    // Story 7.9's value list. It floats OVER the pane, so "is it there" is the
    // whole question — a popup the same colour as what it covers is a popup the
    // reader cannot tell from the rows behind it. Same tokens as the value chip
    // above, which is deliberate: one raised surface in this pane, not two.
    what: 'the value list — the popup that says what a key accepts',
    selector: '.combo-popup',
    edge: ['border', 'border-color'],
    on: 'editor-background',
  },
];

/**
 * The `--vscode-*` chain a selector paints one of `properties` with.
 *
 * `var(--vscode-editorWidget-border, var(--vscode-panel-border))` is
 * `['editorWidget-border', 'panel-border']` — the fallback order, which is what
 * decides the rendered colour in a theme that defines only the second one.
 * Later rules win, matching the cascade at equal specificity.
 */
export function paintChain(css: string, selector: string, properties: string[]): string[] {
  let chain: string[] = [];
  for (const block of ruleBlocks(css)) {
    if (block.selector !== selector) {
      continue;
    }
    for (const decl of block.declarations) {
      if (properties.includes(decl.property) && decl.tokens.length > 0) {
        chain = decl.tokens;
      }
    }
  }
  return chain;
}

/** The first token of a fallback chain that a theme actually defines. */
export function firstDefined(
  theme: Record<string, string>,
  chain: string[],
): { token: string; value: string } | undefined {
  for (const token of chain) {
    const value = theme[token];
    if (value !== undefined) {
      return { token, value };
    }
  }
  return undefined;
}

export interface Separation {
  /** How the surface is told apart, or `null` if it is not. */
  by: 'fill' | 'contrast border' | null;
  fill?: string;
  pane?: string;
  edge?: string;
  why: string;
}

/**
 * Whether a raised surface is distinguishable from its pane in one theme.
 *
 * Two ways, and one of them has to hold:
 *
 *   a. its FILL differs from the pane's; or
 *   b. the theme defines `contrastBorder` and its edge resolves to exactly
 *      that. This is not a loophole — it is how VS Code itself separates
 *      surfaces in the high-contrast themes, where every background is the same
 *      colour on purpose and the border does all the work.
 *
 * An edge that merely differs from the pane is NOT accepted. That is precisely
 * what the broken chip had: `input-border` is a pale hairline in Light+, and a
 * hairline is what was left when the fill vanished. Accepting it would make
 * this check pass on the exact rule it was written for.
 *
 * What this does NOT do is judge whether a difference is big ENOUGH to see —
 * that is a rendered-pixel question, and the screenshot harness is what answers
 * it. This refuses the case where there is no difference at all, which is the
 * case that shipped.
 */
export function separates(
  css: string,
  raised: RaisedSurface,
  theme: Record<string, string>,
): Separation {
  const surfaceChain = paintChain(css, raised.selector, ['background', 'background-color', 'fill']);
  const edgeChain = paintChain(css, raised.selector, raised.edge);
  const fill = firstDefined(theme, surfaceChain);
  const edge = firstDefined(theme, edgeChain);
  const pane = theme[raised.on];
  const contrast = theme['contrastBorder'];
  const same = (a?: string, b?: string): boolean =>
    a !== undefined && b !== undefined && a.toUpperCase() === b.toUpperCase();

  if (fill === undefined) {
    return { by: null, why: `${raised.selector} declares no surface token this theme defines` };
  }
  if (!same(fill.value, pane)) {
    return {
      by: 'fill',
      fill: fill.value,
      pane,
      edge: edge?.value,
      why: `fill ${fill.value} against pane ${pane}`,
    };
  }
  if (edge !== undefined && contrast !== undefined && same(edge.value, contrast)) {
    return {
      by: 'contrast border',
      fill: fill.value,
      pane,
      edge: edge.value,
      why: `fill and pane are both ${pane}; the theme's contrastBorder ${edge.value} carries it`,
    };
  }
  return {
    by: null,
    fill: fill.value,
    pane,
    edge: edge?.value,
    why:
      `${raised.selector} is ${fill.value} on a pane of ${pane} — the same colour — and its ` +
      `edge (${edge?.value ?? 'none'}) is not this theme's contrast border`,
  };
}

/**
 * Ink tokens whose surface could not be derived from the stylesheet at all.
 *
 * Reported rather than failed: this is the residue shape 3 above describes, and
 * naming it is what keeps the derived check from reading stronger than it is.
 */
export function underivedInk(css: string): string[] {
  const derived = new Set(derivedPairings(css).map((p) => p.ink));
  return inkTokens(css).filter((t) => !derived.has(t));
}

/**
 * Selectors that remove an outline without restoring one on `:focus-visible`.
 *
 * `outline: none` is the single most common way an accessibility floor is
 * lost, and it is always written for a good local reason. It is allowed here
 * only where the same base selector puts a ring back.
 */
export function outlineGaps(css: string): string[] {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '');
  const removed: string[] = [];
  const restored = new Set<string>();
  for (const rule of stripped.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
    const selector = rule[1].trim().replace(/\s+/g, ' ');
    const body = rule[2];
    // The value is read whole rather than lookahead-matched: `\s*(?!none)`
    // backtracks the whitespace away and cheerfully accepts `outline: none`,
    // which made this check pass on the exact input it exists to reject.
    const outline = body.match(/outline\s*:\s*([^;]+)/);
    if (outline === null) {
      continue;
    }
    if (outline[1].trim().startsWith('none')) {
      removed.push(selector);
    } else if (/:focus(?:-visible)?/.test(selector)) {
      restored.add(baseSelector(selector));
    }
  }
  return removed.filter((s) => !restored.has(baseSelector(s)));
}

/** A selector with its pseudo-classes removed: `.x:focus` → `.x`. */
export function baseSelector(selector: string): string {
  return selector
    .split(',')
    .map((s) => s.trim().replace(/::?[a-z-]+(\([^)]*\))?/g, ''))
    .join(',');
}

function lineOf(source: string, index: number): number {
  let line = 1;
  for (let i = 0; i < index && i < source.length; i++) {
    if (source[i] === '\n') {
      line++;
    }
  }
  return line;
}

function escapeIdent(name: string): string {
  return name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
