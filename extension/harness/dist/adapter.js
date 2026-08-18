"use strict";
(() => {
  // host/topology.ts
  var MalformedGraphError = class extends Error {
    constructor(detail) {
      super(detail);
      this.detail = detail;
      this.name = "MalformedGraphError";
    }
  };
  function isRecord(v) {
    return typeof v === "object" && v !== null && !Array.isArray(v);
  }
  function readGraph(raw) {
    if (!isRecord(raw)) {
      throw new MalformedGraphError("the core returned something that is not an object");
    }
    for (const key of ["nodes", "edges", "cycles", "dangling", "profiles"]) {
      if (!Array.isArray(raw[key])) {
        throw new MalformedGraphError(`the core's topology has no "${key}" array`);
      }
    }
    const nodes = raw.nodes.filter(
      (n) => isRecord(n) && typeof n.id === "string" && n.id !== "" && typeof n.kind === "string"
    );
    if (nodes.length !== raw.nodes.length) {
      throw new MalformedGraphError("the core returned a node with no id or no kind");
    }
    const known = new Set(nodes.map((n) => n.id));
    const edges = [];
    let droppedEdges = 0;
    for (const e of raw.edges) {
      if (!isRecord(e) || typeof e.from !== "string" || typeof e.to !== "string") {
        throw new MalformedGraphError("the core returned an edge with no endpoints");
      }
      if (!known.has(e.from) || !known.has(e.to)) {
        droppedEdges++;
        continue;
      }
      edges.push(e);
    }
    return {
      graph: {
        profiles: raw.profiles,
        nodes,
        edges,
        cycles: raw.cycles,
        dangling: raw.dangling,
        max_layer: typeof raw.max_layer === "number" ? raw.max_layer : 0
      },
      droppedEdges
    };
  }

  // shared/join.ts
  function pathWithin(path, id) {
    if (id === "") {
      return true;
    }
    if (path === id) {
      return true;
    }
    if (!path.startsWith(id)) {
      return false;
    }
    const next = path.charAt(id.length);
    return next === "." || next === "[";
  }
  function findingsFor(findings, id) {
    const target = id ?? "";
    return findings.filter(
      (f) => f.subjects.some((s) => pathWithin(s, target)) || f.anchors.some((a) => pathWithin(a.path, target))
    );
  }
  function severityCounts(findings, nodeIds) {
    const out = {};
    for (const id of nodeIds) {
      const mine = findingsFor(findings, id);
      if (mine.length === 0) {
        continue;
      }
      const count = { error: 0, warning: 0, hint: 0 };
      for (const f of mine) {
        if (f.severity === "error" || f.severity === "warning" || f.severity === "hint") {
          count[f.severity]++;
        }
      }
      out[id] = count;
    }
    return out;
  }

  // host/inspect.ts
  var MalformedSchemaError = class extends Error {
    constructor(detail) {
      super(detail);
      this.detail = detail;
      this.name = "MalformedSchemaError";
    }
  };
  function isRecord2(v) {
    return typeof v === "object" && v !== null && !Array.isArray(v);
  }
  function readSchema(raw) {
    if (!isRecord2(raw)) {
      throw new MalformedSchemaError("the core returned something that is not an object");
    }
    for (const key of ["files", "profiles"]) {
      if (!Array.isArray(raw[key])) {
        throw new MalformedSchemaError(`the core's schema answer has no "${key}" array`);
      }
    }
    if (typeof raw.schema_commit !== "string" || raw.schema_commit === "") {
      throw new MalformedSchemaError("the core did not say which schema revision the list came from");
    }
    const node = readNode(raw.node);
    return {
      path: typeof raw.path === "string" ? raw.path : "",
      schema_commit: raw.schema_commit,
      compose_version: typeof raw.compose_version === "string" ? raw.compose_version : "",
      compose_version_known: raw.compose_version_known === true,
      files: raw.files,
      profiles: raw.profiles,
      version_field: typeof raw.version_field === "string" ? raw.version_field : void 0,
      node
    };
  }
  function readNode(raw) {
    if (!isRecord2(raw)) {
      throw new MalformedSchemaError("the core returned no node to inspect");
    }
    if (!Array.isArray(raw.fields)) {
      throw new MalformedSchemaError('the node has no "fields" array');
    }
    for (const f of raw.fields) {
      if (!isRecord2(f) || typeof f.key !== "string" || typeof f.declared !== "boolean") {
        throw new MalformedSchemaError("a field has no key, or does not say whether it is declared");
      }
    }
    return {
      path: typeof raw.path === "string" ? raw.path : "",
      schema: typeof raw.schema === "string" ? raw.schema : "",
      known: raw.known === true,
      title: typeof raw.title === "string" ? raw.title : void 0,
      fields: raw.fields,
      declared_count: typeof raw.declared_count === "number" ? raw.declared_count : 0,
      available_count: typeof raw.available_count === "number" ? raw.available_count : 0,
      free_form: raw.free_form === true,
      missing: raw.missing === true
    };
  }
  function readReport(raw) {
    if (!isRecord2(raw) || !Array.isArray(raw.findings)) {
      throw new MalformedSchemaError('the core returned a report with no "findings" array');
    }
    const findings = [];
    for (const f of raw.findings) {
      if (!isRecord2(f) || typeof f.message !== "string" || !Array.isArray(f.anchors)) {
        throw new MalformedSchemaError("a finding has no message or no anchors");
      }
      if (f.anchors.length === 0) {
        throw new MalformedSchemaError("a finding carries no position");
      }
      findings.push(f);
    }
    return {
      path: typeof raw.path === "string" ? raw.path : "",
      profiles: Array.isArray(raw.profiles) ? raw.profiles : [],
      findings
    };
  }

  // host/staging.ts
  var RULE_DOCKERFILE_MISSING = "build-dockerfile-missing";
  function fileName(file) {
    const parts = file.replace(/\\/g, "/").split("/");
    return parts[parts.length - 1] ?? file;
  }
  function saveLabel(file) {
    return `Save to ${fileName(file)}`;
  }
  function missingDockerfileNodes(findings) {
    const out = /* @__PURE__ */ new Set();
    for (const f of findings) {
      if (f.rule !== RULE_DOCKERFILE_MISSING) {
        continue;
      }
      for (const anchor of f.anchors) {
        if (anchor.path) {
          out.add(anchor.path);
        }
      }
    }
    return Array.from(out);
  }

  // harness/adapter.ts
  var IMAGE_UPGRADE = {
    reference: "nginx:1.27",
    repository: "library/nginx",
    display: "nginx",
    tag: "1.27",
    state: "ok",
    message: "nginx:1.29 is a minor upgrade in the same family.",
    age: "14 months old",
    age_days: 425,
    pill: "nginx:1.29 \xB7 minor \xB7 40MB smaller",
    candidate: {
      reference: "nginx:1.29",
      tag: "1.29",
      kind: "minor",
      size_delta: -41943040,
      has_size: true
    }
  };
  var IMAGE_RATE_LIMITED = {
    reference: "nginx:1.27",
    repository: "library/nginx",
    display: "nginx",
    tag: "1.27",
    state: "rate-limited",
    message: "Docker Hub is rate limiting this address. The limit is a hundred and eighty requests a minute and it is shared by everyone behind your address, so this is usually a busy office rather than anything you did. It passes on its own."
  };
  function stackMessages(f, key, diagnoseKey, file) {
    const { graph } = readGraph(f[key]);
    const report = readReport(f[diagnoseKey]);
    return { graph, findings: report.findings, file };
  }
  function inspection(f, schemaKey, id, name, kind, findings, extra = {}) {
    return {
      id,
      name,
      kind,
      schema: readSchema(f[schemaKey]),
      findings: findingsFor(findings, id),
      staged: extra.staged ?? [],
      opened: extra.opened ?? [],
      pending: extra.pending ?? {}
    };
  }
  window.ComposureHarness = {
    messages(f, scenario) {
      const out = [];
      const webstack = f.paths.webstack;
      if (scenario === "empty") {
        const { graph: graph2, findings: findings2, file: file2 } = stackMessages(f, "topology.empty", "diagnose.empty", f.paths.empty);
        out.push({ type: "clearFailure" });
        out.push({ type: "empty", file: file2 });
        out.push({
          type: "inspection",
          file: file2,
          inspection: inspection(f, "schema.empty", null, "compose.yaml", "stack", findings2)
        });
        out.push({ type: "pendingCleared", file: file2 });
        return out;
      }
      if (scenario === "dockerfile" || scenario === "dockerfile-upgrade") {
        const file2 = f.paths.dockerfile;
        out.push({ type: "dockerfile", file: file2, form: f.dockerfile, from: null, staged: [] });
        out.push({ type: "pendingCleared", file: file2 });
        if (scenario === "dockerfile-upgrade") {
          out.push({
            type: "imageLookup",
            file: file2,
            key: "stage:0",
            lookup: IMAGE_UPGRADE
          });
        }
        return out;
      }
      const large = scenario === "large";
      const { graph, findings, file } = large ? stackMessages(f, "topology.large", "diagnose.empty", f.paths.large) : stackMessages(f, "topology", "diagnose", webstack);
      out.push({ type: "clearFailure" });
      out.push({
        type: "graph",
        file,
        graph,
        missing: missingDockerfileNodes(findings),
        positions: {},
        selected: scenario === "stack" ? null : "services.web",
        fit: true,
        severities: severityCounts(findings, graph.nodes.map((n) => n.id))
      });
      out.push({
        type: "profiles",
        file,
        declared: readSchema(f["schema.stack"]).profiles,
        active: []
      });
      if (scenario === "stack" || large) {
        out.push({
          type: "inspection",
          file,
          inspection: inspection(f, "schema.stack", null, "compose.yaml", "stack", findings)
        });
        out.push({ type: "pendingCleared", file });
        return out;
      }
      const staged = scenario === "pending" ? ["services.web.image"] : [];
      const pending = scenario === "pending" ? { "services.web.image": "ghcr.io/shipyard/web:2.5.0" } : {};
      out.push({
        type: "inspection",
        file,
        inspection: inspection(f, "schema.web", "services.web", "web", "service", findings, {
          staged,
          pending
        })
      });
      if (scenario === "pending") {
        const p = f.preview;
        out.push({
          type: "pending",
          file,
          count: 1,
          diff: p.diff,
          added: p.added ?? 1,
          removed: p.removed ?? 1,
          saveLabel: saveLabel(file)
        });
      } else {
        out.push({ type: "pendingCleared", file });
      }
      if (scenario === "upgrade" || scenario === "ratelimited") {
        out.push({
          type: "imageLookup",
          file,
          key: "services.web.image",
          lookup: scenario === "upgrade" ? IMAGE_UPGRADE : IMAGE_RATE_LIMITED
        });
      }
      return out;
    }
  };
})();
