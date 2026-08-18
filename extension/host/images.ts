// Image discovery, host side — Epic 8.
//
// EVERY OTHER CAPABILITY IN THIS EXTENSION IS A PURE FUNCTION OF FILES ON DISK.
// This one is not, and every decision in this module follows from that
// (DECISIONS.md 22):
//
//   - a lookup is never awaited by anything that draws a pane. `panel.ts` posts
//     the inspection first and starts this afterwards, unawaited, so a pane
//     renders identically on a machine with no route to the internet;
//   - a bad or unknown answer is DROPPED rather than rendered. A core from the
//     future with a tenth state would otherwise put a sentence this build does
//     not understand under a reader's image field;
//   - nothing here computes anything about an image. The state, the sentence
//     and the pill string are all composed in Go, because the CLI, the JSON and
//     this pane wording one 429 three ways is exactly what a shared core exists
//     to prevent.
//
// It imports nothing from `vscode`, so the tests can drive it.

import type { ImageLookup, ImageSearchAnswer, ImageState, SchemaField } from '../shared/protocol';

/**
 * How long a lookup may take before it is abandoned.
 *
 * Deliberately shorter than `REQUEST_TIMEOUT_MS`, which bounds the requests a
 * pane cannot be drawn without. This one is optional: a socket held open for
 * thirty seconds on a dead network is thirty seconds spent on a fact nobody is
 * waiting for, and the core has its own five-second deadline underneath.
 */
export const IMAGE_LOOKUP_TIMEOUT_MS = 8000;

/** The states this build knows how to render. A wire slug outside it is dropped. */
const STATES: readonly ImageState[] = [
  'ok',
  'current',
  'offline',
  'rate-limited',
  'not-found',
  'other-registry',
  'not-comparable',
  'disabled',
  'cancelled',
];

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * The staging key for a Dockerfile stage's base image.
 *
 * The SAME key `host/staging.ts stageKey` uses, deliberately: the pill stages a
 * `set_base_image` under it, and two spellings would let a pill and its own
 * staged edit address different things. It is duplicated as a one-line function
 * rather than imported because `staging.ts` is about held intent and this file
 * is about a network answer, and the test pins the literal on both sides.
 */
export function stageLookupKey(stage: number): string {
  return `stage:${stage}`;
}

/** One thing worth asking Docker Hub about: where it is, and what it says. */
export interface ImageAsk {
  /** The join key — a config path, or `stage:0`. */
  key: string;
  ref: string;
}

/**
 * The image references in one inspection, if any.
 *
 * The rule is deliberately narrow: the key is spelled `image` and the value is
 * a non-empty scalar. It is NOT "any key whose name ends in image" — the walk
 * is recursive, and a looser rule would spend a share of a shared rate limit on
 * whatever a nested `image:` happened to hold.
 *
 * A pane with nothing to ask about asks nothing at all. That is not an
 * optimisation: Docker Hub's limit is 180 a minute for everyone behind one
 * address, and a request made for no reason is one a colleague then cannot make.
 */
export function imageLookupKeys(fields: SchemaField[] | undefined): ImageAsk[] {
  const out: ImageAsk[] = [];
  const walk = (list: SchemaField[] | undefined): void => {
    for (const f of list ?? []) {
      const text = f.value?.kind === 'scalar' ? f.value.text.trim() : '';
      if (f.key === 'image' && text !== '' && f.path !== '') {
        out.push({ key: f.path, ref: text });
      }
      walk(f.children);
    }
  };
  walk(fields);
  return out;
}

/**
 * Validates one `image/lookup` answer, returning null for anything this build
 * cannot render.
 *
 * Null rather than a best effort. The failure mode of a lenient read here is
 * not a crash — it is a sentence from a core this extension does not understand
 * rendered under a reader's image field as though we had checked it, which is
 * this project's characteristic defect in the one place it is newest.
 */
export function readLookup(raw: unknown): ImageLookup | null {
  if (!isRecord(raw)) {
    return null;
  }
  if (typeof raw.state !== 'string' || !STATES.includes(raw.state as ImageState)) {
    return null;
  }
  if (typeof raw.message !== 'string' || typeof raw.reference !== 'string') {
    return null;
  }
  const state = raw.state as ImageState;
  const candidate = isRecord(raw.candidate) &&
    typeof raw.candidate.reference === 'string' &&
    raw.candidate.reference !== '' &&
    typeof raw.candidate.tag === 'string'
    ? {
        reference: raw.candidate.reference,
        tag: raw.candidate.tag,
        kind: typeof raw.candidate.kind === 'string' ? raw.candidate.kind : '',
        size_delta: typeof raw.candidate.size_delta === 'number' ? raw.candidate.size_delta : undefined,
        has_size: raw.candidate.has_size === true,
      }
    : undefined;
  // `ok` MEANS there is something to offer. Without a candidate the pill is a
  // button that stages nothing, which is worse than no pill.
  if (state === 'ok' && candidate === undefined) {
    return null;
  }
  return {
    reference: raw.reference,
    repository: typeof raw.repository === 'string' ? raw.repository : '',
    display: typeof raw.display === 'string' ? raw.display : '',
    tag: typeof raw.tag === 'string' ? raw.tag : '',
    state,
    message: raw.message,
    age: typeof raw.age === 'string' ? raw.age : undefined,
    age_days: typeof raw.age_days === 'number' ? raw.age_days : undefined,
    pill: typeof raw.pill === 'string' ? raw.pill : undefined,
    candidate,
  };
}

/** Validates one `image/search` answer. A nameless result is dropped; the rest stay. */
export function readSearch(raw: unknown): ImageSearchAnswer | null {
  if (!isRecord(raw) || typeof raw.state !== 'string' || !STATES.includes(raw.state as ImageState)) {
    return null;
  }
  if (typeof raw.message !== 'string' || !Array.isArray(raw.results)) {
    return null;
  }
  const results = raw.results
    .filter((r): r is Record<string, unknown> => isRecord(r) && typeof r.name === 'string' && r.name !== '')
    .map((r) => ({
      name: r.name as string,
      description: typeof r.description === 'string' ? r.description : undefined,
      stars: typeof r.stars === 'number' ? r.stars : 0,
      pulls_display: typeof r.pulls_display === 'string' ? r.pulls_display : undefined,
      official: r.official === true,
      badge: typeof r.badge === 'string' ? r.badge : undefined,
      architectures: Array.isArray(r.architectures)
        ? (r.architectures.filter((a) => typeof a === 'string') as string[])
        : undefined,
    }));
  return {
    query: typeof raw.query === 'string' ? raw.query : '',
    state: raw.state as ImageState,
    message: raw.message,
    results,
  };
}

/**
 * Whether a state is worth putting in front of a reader.
 *
 * Everything except `cancelled`, which means they selected something else — a
 * sentence about a question they stopped asking is noise, and it would arrive
 * on the pane they moved TO.
 *
 * Note what is NOT filtered out: `offline`, `rate-limited` and `other-registry`
 * all say something. Silence there reads as "there is nothing newer", which is
 * the confident wrong answer in the exact place a reader decides whether to
 * upgrade.
 */
export function isSayable(state: ImageState): boolean {
  return state !== 'cancelled';
}
