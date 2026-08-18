// Which node the cursor is inside — story 4.3's second direction.
//
// `host/locate.ts` was extracted from the `onDidChangeTextEditorSelection`
// callback for exactly this purpose, and the test was never written. Nothing in
// the product imported `nodeAtCursor`, `nodeRanges`, `isOwner` or `sourceFiles`
// from anywhere but panel.ts, so all four could be made to return null, [] or
// false with a green suite: the graph would simply stop following the cursor,
// which is a feature disappearing rather than a failure appearing.
//
// The arithmetic is right. What it lacked was anything that would say so.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { isOwner, nodeAtCursor, nodeRanges, sourceFiles } from './locate';
import type { GraphNode, NodeKind } from '../shared/protocol';

const FILE = '/w/compose.yaml';
const OTHER = '/w/compose.override.yaml';

function at(line: number, file = FILE): GraphNode['origin'] {
  return { file, line, column: 3, step: 0 };
}

function node(id: string, kind: NodeKind, line: number, file = FILE): GraphNode {
  return {
    id,
    kind,
    name: id.split('.').pop() ?? id,
    origin: at(line, file),
    declared: true,
    external: false,
    profiles: [],
    layer: 0,
  };
}

/**
 * The shape of a real compose file, as the core reports it:
 *
 * ```
 *  1  services:
 *  2    web:
 *  3      image: nginx
 *  4      ports:
 *  5        - "8080:80"
 *  6        - "8443:443"
 *  7      depends_on: [api]
 *  8    api:
 *  9      image: api
 * 10      build:
 * 11        context: .
 * 12  networks:
 * 13    backend:
 * 14      driver: bridge
 * 15  volumes:
 * 16    data:
 * ```
 */
const NODES: GraphNode[] = [
  node('services.web', 'service', 2),
  node('services.web.ports[0]', 'port', 5),
  node('services.web.ports[1]', 'port', 6),
  node('services.api', 'service', 8),
  node('services.api.build', 'dockerfile', 10),
  node('networks.backend', 'network', 13),
  node('volumes.data', 'volume', 16),
];

describe('which node owns a range', () => {
  it('calls a top-level declaration an owner and nothing else', () => {
    assert.equal(isOwner('services.web'), true);
    assert.equal(isOwner('networks.backend'), true);
    assert.equal(isOwner('services.web.ports[0]'), false, 'a published port would swallow its service');
    assert.equal(isOwner('services.api.build'), false);
    assert.equal(isOwner('services'), false, 'the section key itself is not a node');
  });

  it('runs each owner to the line before the next one in the same section', () => {
    const ranges = nodeRanges(NODES, FILE);
    assert.deepEqual(
      ranges.map((r) => [r.id, r.start, r.end]),
      [
        ['services.web', 2, 7],
        ['services.api', 8, 11],
        ['networks.backend', 13, 14],
        ['volumes.data', 16, Infinity],
      ],
      'the computed ranges no longer match the file they were derived from',
    );
  });

  it('stops a section short of the next section key, which belongs to no service', () => {
    // `services.api` ends at 11, not 12: line 12 is `networks:`, and stretching
    // the last service over it would report a selection for a line that
    // declares nothing. The comment in locate.ts says so; this is the check.
    const ranges = nodeRanges(NODES, FILE);
    const api = ranges.find((r) => r.id === 'services.api')!;
    assert.equal(api.end, 11);
    assert.equal(nodeAtCursor(NODES, FILE, 12), null, 'the `networks:` key itself selected a service');
  });

  it('leaves the last declaration in a file open-ended', () => {
    assert.equal(nodeAtCursor(NODES, FILE, 16), 'volumes.data');
    assert.equal(nodeAtCursor(NODES, FILE, 400), 'volumes.data');
  });

  it('ignores nodes declared in another file', () => {
    const mixed = [...NODES, node('services.extra', 'service', 4, OTHER)];
    assert.deepEqual(
      nodeRanges(mixed, OTHER).map((r) => r.id),
      ['services.extra'],
    );
  });

  it('keeps a degenerate range covering at least its own line', () => {
    // Two declarations on adjacent lines across a section boundary: the
    // arithmetic would otherwise produce an end BEFORE the start.
    const tight = [node('services.only', 'service', 4), node('networks.n', 'network', 5)];
    const ranges = nodeRanges(tight, FILE);
    assert.deepEqual(ranges[0], { id: 'services.only', file: FILE, start: 4, end: 4 });
    assert.equal(nodeAtCursor(tight, FILE, 4), 'services.only');
  });
});

describe('the node the cursor is in', () => {
  it('selects the service the cursor is inside the body of', () => {
    for (const line of [2, 3, 4, 7]) {
      assert.equal(nodeAtCursor(NODES, FILE, line), 'services.web', `line ${line}`);
    }
    for (const line of [8, 9]) {
      assert.equal(nodeAtCursor(NODES, FILE, line), 'services.api', `line ${line}`);
    }
  });

  // The criterion in one assertion: with the cursor on `- "8080:80"` the reader
  // is looking at that published port, and selecting the service instead is an
  // answer to a question they did not ask.
  it('lets an exact hit on a nested declaration beat the owner containing it', () => {
    assert.equal(nodeAtCursor(NODES, FILE, 5), 'services.web.ports[0]');
    assert.equal(nodeAtCursor(NODES, FILE, 6), 'services.web.ports[1]');
    assert.equal(nodeAtCursor(NODES, FILE, 10), 'services.api.build');
  });

  it('chooses the innermost of two nested nodes declared on the same line', () => {
    const nested = [
      node('services.web', 'service', 2),
      node('services.web.build', 'dockerfile', 5),
      node('services.web.build.args', 'config', 5),
    ];
    assert.equal(nodeAtCursor(nested, FILE, 5), 'services.web.build.args');
  });

  it('selects nothing for a line between sections, and nothing above the first', () => {
    assert.equal(nodeAtCursor(NODES, FILE, 12), null, 'a section key selected the service above it');
    assert.equal(nodeAtCursor(NODES, FILE, 15), null);
    assert.equal(nodeAtCursor(NODES, FILE, 1), null, '`services:` itself selected a service');
  });

  it('selects nothing in a file the graph declares nothing in', () => {
    assert.equal(nodeAtCursor(NODES, '/w/somewhere/else.yaml', 3), null);
    assert.equal(nodeAtCursor(NODES, '', 3), null);
    assert.equal(nodeAtCursor(NODES, FILE, 0), null);
    assert.equal(nodeAtCursor(NODES, FILE, -1), null);
  });

  it('answers per file when the same lines exist in two of them', () => {
    const mixed = [...NODES, node('services.extra', 'service', 3, OTHER)];
    assert.equal(nodeAtCursor(mixed, FILE, 3), 'services.web');
    assert.equal(nodeAtCursor(mixed, OTHER, 3), 'services.extra');
  });
});

describe('the files a drawn stack is made of — story 4.3’s third criterion', () => {
  it('names every file the graph declares something in, sorted and de-duplicated', () => {
    const mixed = [...NODES, node('services.extra', 'service', 3, OTHER)];
    assert.deepEqual(sourceFiles(mixed), [OTHER, FILE].sort());
    assert.equal(sourceFiles(NODES).length, 1, 'the watcher set is not de-duplicated');
  });

  it('is stable across redraws, so watchers are not churned', () => {
    const shuffled = [...NODES].reverse();
    assert.deepEqual(sourceFiles(shuffled), sourceFiles(NODES));
  });

  it('drops a node with no origin rather than watching an empty path', () => {
    const broken = [
      ...NODES,
      { ...node('services.ghost', 'service', 1), origin: { file: '', line: 0, column: 0, step: 0 } },
    ];
    assert.deepEqual(sourceFiles(broken), [FILE]);
  });
});
