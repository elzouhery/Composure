// Semantic grouping for the inspector — Direction A's named groups.
//
// The pane used to group STRUCTURALLY: a declared key became its own group only
// if its value was a mapping with children. For a normal service that is zero
// groups, so the whole pane rendered as one section headed with the literal
// word "declared" followed by a flat run of ninety keys, and 5.2's "each group
// ends with the keys the schema permits HERE" was satisfied by a single
// ninety-key list at the foot. The mockup has `Image`, `Environment`, `Health`.
//
// So this file is a map from key name to group. That is exactly the thing AD-20
// forbids one level down — a hand-written list of Compose keys that falls
// behind the specification — and the objection is real, so the design answers
// it rather than ignoring it:
//
//   * The map decides ONLY which heading a key is printed under. It is not a
//     filter and it is not a list of what exists: every field handed in comes
//     back out, and `groupFields` asserts nothing about which keys it knows.
//   * A key the map has never heard of lands in `other`, which is a real group
//     with a real heading and its own `available, not set` line. A key added to
//     the Compose specification tomorrow is therefore SHOWN tomorrow; what it
//     is not is *categorised*, which is a cosmetic degradation and not a
//     disappearance.
//   * `webview/groups.test.ts` asserts the round trip — every field in, every
//     field out, exactly once — including over a field set full of names this
//     map has never heard of. The failure mode the structural rule was
//     protecting against is closed by that test rather than by refusing to
//     have headings at all.
//
// Nothing here decides what a key MEANS, renders anything, or reaches a file.

import type { SchemaField } from '../shared/protocol';

/** The catch-all. Every key the map has not heard of is printed here. */
export const OTHER = 'other';

/**
 * The order groups are printed in — what the reader reads top to bottom.
 *
 * Identity first (what image is this), then what it runs, then how it is
 * configured, then how it is reached, then what it can see, then whether it is
 * healthy, then what it is allowed to consume. `other` is always last, so a key
 * nobody has categorised is at the foot rather than interrupting the middle.
 */
export const GROUP_ORDER = [
  'stack',
  'services',
  'image',
  'command',
  'environment',
  'network',
  'storage',
  'health',
  'resources',
  'metadata',
  OTHER,
];

/**
 * Key name to group.
 *
 * Keyed by the key's own name, never by its config path, so the same name at
 * the top level and inside a service lands in the same place — `networks:` is
 * about the network either way, and a reader looking for it should not have to
 * learn two layouts.
 */
export const GROUP_OF: Record<string, string> = {
  // Top level.
  name: 'stack',
  include: 'stack',
  services: 'services',

  // What image this is.
  image: 'image',
  build: 'image',
  platform: 'image',
  pull_policy: 'image',

  // What it runs.
  command: 'command',
  entrypoint: 'command',
  working_dir: 'command',
  user: 'command',
  init: 'command',
  tty: 'command',
  stdin_open: 'command',

  // How it is configured.
  environment: 'environment',
  env_file: 'environment',
  labels: 'environment',
  annotations: 'environment',

  // How it is reached.
  ports: 'network',
  expose: 'network',
  networks: 'network',
  network_mode: 'network',
  hostname: 'network',
  domainname: 'network',
  dns: 'network',
  dns_search: 'network',
  dns_opt: 'network',
  extra_hosts: 'network',
  links: 'network',
  external_links: 'network',
  mac_address: 'network',
  ipc: 'network',
  uts: 'network',

  // What it can see.
  volumes: 'storage',
  volumes_from: 'storage',
  tmpfs: 'storage',
  configs: 'storage',
  secrets: 'storage',
  storage_opt: 'storage',
  read_only: 'storage',

  // Whether it is healthy, and what happens when it is not.
  healthcheck: 'health',
  depends_on: 'health',
  restart: 'health',
  stop_signal: 'health',
  stop_grace_period: 'health',

  // What it is allowed to consume.
  deploy: 'resources',
  cpus: 'resources',
  cpu_count: 'resources',
  cpu_percent: 'resources',
  cpu_shares: 'resources',
  cpu_period: 'resources',
  cpu_quota: 'resources',
  cpu_rt_runtime: 'resources',
  cpu_rt_period: 'resources',
  cpuset: 'resources',
  mem_limit: 'resources',
  mem_reservation: 'resources',
  mem_swappiness: 'resources',
  memswap_limit: 'resources',
  shm_size: 'resources',
  pids_limit: 'resources',
  oom_kill_disable: 'resources',
  oom_score_adj: 'resources',
  ulimits: 'resources',
  cap_add: 'resources',
  cap_drop: 'resources',
  privileged: 'resources',
  security_opt: 'resources',
  userns_mode: 'resources',
  cgroup: 'resources',
  cgroup_parent: 'resources',
  devices: 'resources',
  device_cgroup_rules: 'resources',
  group_add: 'resources',
  sysctls: 'resources',
  blkio_config: 'resources',
  isolation: 'resources',
  runtime: 'resources',

  // What it is called and when it exists.
  container_name: 'metadata',
  profiles: 'metadata',
  scale: 'metadata',
  develop: 'metadata',
  provider: 'metadata',
  attach: 'metadata',
  credential_spec: 'metadata',
  logging: 'metadata',
  label_file: 'metadata',
  post_start: 'metadata',
  pre_stop: 'metadata',
  use_api_socket: 'metadata',
};

/** The group a key is printed under. Never throws and never returns empty. */
export function groupFor(key: string): string {
  return GROUP_OF[key] ?? OTHER;
}

/**
 * One printed section: a heading, the rows under it, and the keys the schema
 * permits here that the file does not declare.
 *
 * `available` is split per group HERE, and printed as one block at the foot of
 * the pane — `inspector.ts availableBlock`, which keeps the runs in this order
 * and labels each with the group name. The split still carries the teaching 5.2
 * asks for (`ulimits` arrives under `resources`); what it no longer carries is
 * ten headings, ten dashed rules and ten `You can also set` leads, five of them
 * under a group that introduced no fields at all. See the comment at the render
 * loop for the measurement that moved it.
 */
export interface FieldGroup {
  label: string;
  /** Rendered as fields: declared, or opened by the reader, or staged. */
  fields: SchemaField[];
  /** Rendered as the group's `available, not set` line. */
  available: SchemaField[];
}

/**
 * Splits a node's fields into printed groups.
 *
 * A field is RENDERED (rather than offered) when the file declares it, or when
 * the reader has opened or staged it — an opened key is a field with the cursor
 * in it, which is the whole of story 5.2's last criterion.
 *
 * Every field handed in comes back out exactly once. That is the property
 * `groups.test.ts` holds this file to, and it is what makes a map of key names
 * safe: the map can be out of date, but it cannot lose a key.
 */
export function groupFields(
  fields: SchemaField[],
  rendered: (field: SchemaField) => boolean,
): FieldGroup[] {
  const byLabel = new Map<string, FieldGroup>();
  const order: string[] = [];
  for (const f of fields) {
    const label = groupFor(f.key);
    let group = byLabel.get(label);
    if (!group) {
      group = { label, fields: [], available: [] };
      byLabel.set(label, group);
      order.push(label);
    }
    if (rendered(f)) {
      group.fields.push(f);
    } else {
      group.available.push(f);
    }
  }
  // Known groups in the reading order above; anything the order does not name
  // keeps the order the fields arrived in, which is file order.
  const known = GROUP_ORDER.filter((l) => byLabel.has(l));
  const rest = order.filter((l) => !GROUP_ORDER.includes(l));
  return [...known, ...rest].map((l) => byLabel.get(l)!);
}
