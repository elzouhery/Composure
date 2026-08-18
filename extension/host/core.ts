// The JSON-RPC client for the Go core.
//
// The core is `composure serve`: a subprocess spoken to over stdio with LSP-style
// `Content-Length` framing — the gopls / rust-analyzer arrangement. A Go
// process and a Node extension host cannot share a runtime, and this boundary
// is what keeps the CLI (and later an MCP server) first-class rather than a
// second implementation.
//
// This module imports nothing from `vscode` on purpose. It is the piece the
// tests drive against a stub process, and a dependency on the editor API would
// make that impossible.

import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import * as path from 'node:path';

/**
 * The wire revision this client speaks. The core reports its own at handshake.
 *
 * Revision 2 is Epic 6: `stack/preview`, `stack/apply` and `stack/dockerfile`.
 * It is bumped rather than treated as an additive change because this client
 * now has a Save button, and a core without an apply method would answer it
 * with method-not-found at the one moment the reader is expecting their file to
 * be written. Refusing at the handshake is the cheaper failure by far.
 *
 * Revision 3 is story 4.4's focus mode: `stack/impact`. Same reasoning — the
 * canvas has a Focus control, and a core without the method would fail at the
 * gesture rather than at the handshake, which puts the version skew in front of
 * the reader instead of in front of us.
 *
 * Revision 4 is `stack/add` — stories 7.3 and 7.4.
 *
 * Revision 5 is `stack/editable` — decision 21. The pane asks it where each
 * value is WRITTEN, which is not always the path it is read from, and stages an
 * insert rather than a replace when the answer is "through a merge key". A core
 * without the method leaves the pane staging a replace against a path with no
 * bytes behind it, which is the defect decision 21 closes, reappearing as a
 * version skew.
 *
 * Revision 6 is `image/lookup` and `image/search` — Epic 8, DECISIONS.md 22.
 * It bites harder than the five before it: this is the first method whose
 * answer the client cannot compute for itself under any circumstances, so a
 * core without it leaves the reader typing into a search box that answers
 * nothing at all. It is also the first method that leaves the machine, which is
 * exactly why the skew belongs at the handshake and not at the keystroke.
 *
 * Revision 7 is `stack/extract` and `stack/extract-apply` — story 9.3,
 * DECISIONS.md 25. The core has had both methods since Epic 9's engine half and
 * the revision was deliberately held at 6, because no client called them; this
 * bump is that client arriving. It bites in a way none of the six before it
 * could: the move writes TWO files, so a client offering `Move to a variable`
 * against a core that cannot perform it would have the reader approve two diffs
 * and get neither — and the half-done version of that state is a compose file
 * saying `${POSTGRES_PASSWORD}` with no `.env` to define it, which `docker
 * compose` resolves to the empty string without a word.
 *
 * Revision 8 is the first that is not a new method. `Finding.fix` gained a
 * `remedy` — story 9.5, DECISIONS.md 26 — the field that lets the
 * plaintext-credential rule finally say what it has known since Epic 3: that
 * `composure extract` takes the same config path the finding anchors, so the
 * finding and the remedy are one address. A field is a wire shape exactly as a
 * method is, and a client rendering `and: composure extract …` against a core that
 * emits no remedy shows an empty affordance in the one surface whose whole job
 * is to be trusted. `stack/extract-arg` — story 9.4, the Dockerfile `ARG`
 * equivalent of the move — rides along; on its own it would not have earned a
 * bump, by revision 7's own rule that a method with no client is not one.
 *
 * Revision 9 is story 9.6, DECISIONS.md 28: the two-file write joining the
 * staleness contract. `stack/extract-apply` gained `expect` and `expect_env` on
 * the request and `env_expect` on the response; `stack/extract-arg-apply`
 * gained `expect`. Every field is optional, so the wire is additive and the
 * bump is not about parsing — it is about what a client that does not send them
 * gets, which is no staleness protection at all on the widest write in the
 * product, while every other staged write has had it since AD-19.
 *
 * THIS CLIENT SENDS THEM, and that sentence is load-bearing rather than
 * decorative. Until 2026-08-15 it did not: the extension pinned this constant
 * at 9 and sent neither field on either apply, which made it exactly the client
 * the bump exists to keep out — inside the handshake's own boundary, passing
 * it. It also made `panel.ts`'s `classify(err) === 'stale'` branch on both
 * apply paths unreachable, because a request that asserts nothing can never be
 * refused as stale. `host/edit.ts`'s `expectOf` records the assertion from the
 * preview the reader approved, and `host/panelbehaviour.test.ts` asserts the
 * apply params carry it. Raising this number without sending whatever the new
 * revision adds repeats the defect.
 */
export const PROTOCOL_REVISION = 9;

/** How long the handshake may take before the core is declared dead. Bounded, never infinite. */
export const HANDSHAKE_TIMEOUT_MS = 8000;

/**
 * How long any single request may take before it is abandoned.
 *
 * Every call is bounded, not just the handshake. A core that accepts
 * `stack/resolve` and never answers would otherwise leave the panel loading
 * forever, and — because the panel coalesces draws while one is in flight —
 * every later redraw would be silently dropped behind it. A named rejection is
 * the only outcome a reader can act on.
 */
export const REQUEST_TIMEOUT_MS = 30000;

/** How much of the core's stderr to keep for a failure banner. */
const STDERR_TAIL_BYTES = 4000;

/** A frame body larger than this is a broken or hostile peer, not a project. */
const MAX_FRAME_BYTES = 64 << 20;

/**
 * The same notion applied to the header block: bytes that contain no CRLFCRLF
 * are not a frame in progress, they are a stream that will never yield one.
 * Without this the buffer grows without bound on a peer that never terminates
 * a header.
 */
const MAX_HEADER_BYTES = 64 << 10;

/** How long to wait after SIGTERM before SIGKILL. A core must not outlive its window. */
const KILL_GRACE_MS = 1500;

export type CoreErrorKind = 'core-missing' | 'spawn-failed' | 'core-crashed' | 'protocol' | 'timeout';

/**
 * A failure of the core process itself, as opposed to a file it refused.
 * Typed rather than a bare Error so the panel can name the failure mode
 * instead of printing a stack trace at a reader.
 */
export class CoreError extends Error {
  constructor(
    readonly kind: CoreErrorKind,
    message: string,
    readonly detail: string = '',
  ) {
    super(message);
    this.name = 'CoreError';
  }
}

/**
 * The data the core attaches to a failed request.
 *
 * `line` and `column` are a refused compose file's position; `reason` is a
 * refused EDIT's stable slug — "flow-style", "stale-range", "multi-line". One
 * shape rather than two because one field carries both: the JSON-RPC `data`
 * member, and a client that had to guess which shape it was looking at would
 * guess wrong on the day a new code was added.
 */
export interface RpcErrorData {
  file?: string;
  line?: number;
  column?: number;
  reason?: string;
  written?: boolean;
}

/** A well-formed request the core answered with an error — a refused compose file, typically. */
export class RpcError extends Error {
  constructor(
    readonly code: number,
    message: string,
    readonly data: RpcErrorData | undefined,
  ) {
    super(message);
    this.name = 'RpcError';
  }
}

/** JSON-RPC code the core uses for a compose file it refused. */
export const CODE_RESOLVE_FAILED = -32001;

export interface ExitInfo {
  code: number | null;
  signal: NodeJS.Signals | null;
  /** The tail of the core's stderr, which is where its diagnostics go. */
  stderr: string;
}

export interface CoreOptions {
  binaryPath: string;
  /** Where diagnostics go — the extension's output channel in production. */
  log: (line: string) => void;
  /** Called once, when the process ends for any reason other than dispose(). */
  onExit: (info: ExitInfo) => void;
  /** Per-request bound. Defaults to REQUEST_TIMEOUT_MS; the tests shorten it. */
  requestTimeoutMs?: number;
}

/** The platform triple the shipped binary is filed under: Go's own GOOS-GOARCH. */
export function goTarget(platform: string = process.platform, arch: string = process.arch): string {
  const os: Record<string, string> = { darwin: 'darwin', linux: 'linux', win32: 'windows' };
  const cpu: Record<string, string> = { x64: 'amd64', arm64: 'arm64', arm: 'arm', ia32: '386' };
  return `${os[platform] ?? platform}-${cpu[arch] ?? arch}`;
}

/**
 * Where the core binary for this machine lives.
 *
 * `configured` is the `composure.corePath` setting, which exists so a developer
 * can point at a `go build` output without vendoring a binary into the repo.
 */
export function resolveBinaryPath(
  extensionPath: string,
  configured = '',
  platform: string = process.platform,
  arch: string = process.arch,
): { path: string; target: string; configured: boolean } {
  const target = goTarget(platform, arch);
  if (configured.trim() !== '') {
    return { path: configured.trim(), target, configured: true };
  }
  const exe = platform === 'win32' ? 'composure.exe' : 'composure';
  return { path: path.join(extensionPath, 'bin', target, exe), target, configured: false };
}

/**
 * Decodes a `Content-Length`-framed byte stream into message bodies.
 *
 * Framing rather than newline-delimited JSON because compose values contain
 * newlines and resolved payloads are large. It also degrades safely on a
 * partial write: a short body leaves the decoder waiting rather than parsing
 * half a message as a whole one.
 *
 * Exported for the tests — framing is exactly the kind of code that works on
 * every message until the one that arrives in two chunks.
 */
export class Framer {
  private buffer = Buffer.alloc(0);

  /** Frames one body for the wire. */
  static encode(body: string): Buffer {
    const payload = Buffer.from(body, 'utf8');
    return Buffer.concat([Buffer.from(`Content-Length: ${payload.length}\r\n\r\n`, 'ascii'), payload]);
  }

  /**
   * Feeds bytes in and returns every complete body they completed.
   * Throws when the stream can no longer be resynchronised — a bad
   * Content-Length means every following byte is of unknown provenance.
   */
  push(chunk: Buffer): string[] {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    const out: string[] = [];
    for (;;) {
      const headerEnd = this.buffer.indexOf('\r\n\r\n');
      if (headerEnd < 0) {
        if (this.buffer.length > MAX_HEADER_BYTES) {
          throw new CoreError(
            'protocol',
            `the core sent ${this.buffer.length} bytes without ending a frame header`,
          );
        }
        return out;
      }
      const header = this.buffer.subarray(0, headerEnd).toString('ascii');
      let length = -1;
      for (const line of header.split('\r\n')) {
        const sep = line.indexOf(':');
        if (sep < 0) {
          continue;
        }
        if (line.slice(0, sep).trim().toLowerCase() === 'content-length') {
          length = Number.parseInt(line.slice(sep + 1).trim(), 10);
        }
      }
      if (!Number.isInteger(length) || length < 0) {
        throw new CoreError('protocol', 'the core sent a frame with no usable Content-Length');
      }
      if (length > MAX_FRAME_BYTES) {
        throw new CoreError('protocol', `the core announced a ${length} byte frame`);
      }
      const bodyStart = headerEnd + 4;
      if (this.buffer.length < bodyStart + length) {
        return out; // wait for the rest
      }
      out.push(this.buffer.subarray(bodyStart, bodyStart + length).toString('utf8'));
      this.buffer = this.buffer.subarray(bodyStart + length);
    }
  }
}

interface Pending {
  resolve: (value: unknown) => void;
  reject: (err: Error) => void;
  /** Cleared when the reply lands, so a settled call cannot be timed out later. */
  timer?: ReturnType<typeof setTimeout>;
}

/**
 * One client per window, one resolve per drawn file.
 *
 * The server is stateless — it takes a path and returns a project — so this
 * holds no project state either. A cache here is where a second, divergent
 * model grows.
 */
export class ComposureCore {
  private proc: ChildProcess | undefined;
  private framer = new Framer();
  private pending = new Map<number, Pending>();
  private nextId = 1;
  private stderrTail = '';
  private exited = false;
  private disposed = false;
  private serverVersion = '';

  constructor(private readonly opts: CoreOptions) {}

  get version(): string {
    return this.serverVersion;
  }

  get running(): boolean {
    return this.proc !== undefined && !this.exited;
  }

  /**
   * Spawns the core and completes the handshake.
   *
   * Throws a CoreError for every reachable failure: no binary at the expected
   * path, a binary that will not exec, a handshake that never lands, and a
   * core speaking a protocol revision this client does not.
   */
  async start(timeoutMs = HANDSHAKE_TIMEOUT_MS): Promise<void> {
    if (!existsSync(this.opts.binaryPath)) {
      throw new CoreError(
        'core-missing',
        'the Composure core binary is not where the extension expects it',
        this.opts.binaryPath,
      );
    }

    let proc: ChildProcess;
    try {
      proc = spawn(this.opts.binaryPath, ['serve'], { stdio: ['pipe', 'pipe', 'pipe'] });
    } catch (err) {
      throw new CoreError('spawn-failed', 'the Composure core could not be started', String(err));
    }
    this.proc = proc;

    // A spawn failure surfaces asynchronously on most platforms — a
    // non-executable file does not throw above, it emits 'error' here.
    const spawnFailure = new Promise<never>((_, reject) => {
      proc.once('error', (err) => {
        this.exited = true;
        reject(new CoreError('spawn-failed', 'the Composure core could not be started', String(err)));
      });
    });

    proc.stderr?.on('data', (chunk: Buffer) => {
      const text = chunk.toString('utf8');
      this.stderrTail = (this.stderrTail + text).slice(-STDERR_TAIL_BYTES);
      for (const line of text.split('\n')) {
        if (line.trim() !== '') {
          this.opts.log(line.trimEnd());
        }
      }
    });

    proc.stdout?.on('data', (chunk: Buffer) => this.receive(chunk));

    // A write to a pipe whose reader is gone raises EPIPE on the stream, and an
    // unhandled stream 'error' is an uncaught exception that takes the whole
    // extension host down with it. The exit handler below is what actually
    // reports the failure; this listener exists so the process survives to
    // reach it.
    proc.stdin?.on('error', (err) => {
      this.opts.log(`core stdin: ${String(err)}`);
    });
    proc.stdout?.on('error', (err) => {
      this.opts.log(`core stdout: ${String(err)}`);
    });
    proc.stderr?.on('error', () => undefined);

    proc.once('exit', (code, signal) => {
      this.exited = true;
      const info: ExitInfo = { code, signal, stderr: this.stderrTail.trim() };
      // Every in-flight call rejects. A caller blocked on a dead process is
      // the silent hang this whole design exists to avoid.
      this.rejectAll(
        new CoreError(
          'core-crashed',
          'the Composure core exited',
          describeExit(info),
        ),
      );
      if (!this.disposed) {
        this.opts.onExit(info);
      }
    });

    const handshake = this.request<{ serverName: string; version: string; protocol: number }>(
      'initialize',
      {},
    );
    const timer = new Promise<never>((_, reject) => {
      const t = setTimeout(() => {
        reject(
          new CoreError(
            'spawn-failed',
            'the Composure core did not answer the handshake',
            `no reply within ${timeoutMs}ms${this.stderrTail ? `\n${this.stderrTail.trim()}` : ''}`,
          ),
        );
      }, timeoutMs);
      // Never hold the extension host open on this timer.
      t.unref?.();
      handshake.then(
        () => clearTimeout(t),
        () => clearTimeout(t),
      );
    });

    let result: { serverName: string; version: string; protocol: number };
    try {
      result = await Promise.race([handshake, spawnFailure, timer]);
    } catch (err) {
      this.kill();
      throw err instanceof CoreError
        ? err
        : new CoreError('spawn-failed', 'the Composure core could not be started', String(err));
    }

    if (result.protocol !== PROTOCOL_REVISION) {
      this.kill();
      throw new CoreError(
        'protocol',
        'the Composure core speaks a protocol this extension does not',
        `core protocol ${result.protocol}, extension expects ${PROTOCOL_REVISION}`,
      );
    }
    this.serverVersion = result.version;
    this.opts.log(`core ready: ${result.serverName} ${result.version} (protocol ${result.protocol})`);
  }

  /**
   * Sends one request and resolves with its result.
   *
   * Bounded, never infinite: a reply that does not arrive within
   * `requestTimeoutMs` rejects with a `timeout` CoreError rather than leaving
   * the caller waiting on a process that has stopped answering.
   */
  request<T>(method: string, params: unknown, timeoutMs?: number): Promise<T> {
    if (this.proc === undefined || this.exited) {
      return Promise.reject(
        new CoreError('core-crashed', 'the Composure core is not running', this.stderrTail.trim()),
      );
    }
    const bound = timeoutMs ?? this.opts.requestTimeoutMs ?? REQUEST_TIMEOUT_MS;
    const id = this.nextId++;
    const body = JSON.stringify({ jsonrpc: '2.0', id, method, params });
    return new Promise<T>((resolve, reject) => {
      const entry: Pending = { resolve: resolve as (v: unknown) => void, reject };
      this.pending.set(id, entry);

      const stdin = this.proc?.stdin;
      if (!stdin || stdin.destroyed) {
        this.pending.delete(id);
        reject(new CoreError('core-crashed', 'the Composure core is not accepting input', ''));
        return;
      }
      stdin.write(Framer.encode(body), (err) => {
        if (err) {
          this.settle(id);
          reject(new CoreError('core-crashed', 'the request could not be written to the core', String(err)));
        }
      });

      if (bound > 0) {
        const t = setTimeout(() => {
          if (!this.pending.delete(id)) {
            return;
          }
          reject(
            new CoreError(
              'timeout',
              'the Composure core did not answer in time',
              `${method} had no reply within ${bound}ms${this.stderrTail ? `\n${this.stderrTail.trim()}` : ''}`,
            ),
          );
        }, bound);
        // Never hold the extension host open on this timer.
        t.unref?.();
        entry.timer = t;
      }
    });
  }

  /** Removes a pending call and cancels its timeout. */
  private settle(id: number): Pending | undefined {
    const entry = this.pending.get(id);
    if (!entry) {
      return undefined;
    }
    this.pending.delete(id);
    if (entry.timer !== undefined) {
      clearTimeout(entry.timer);
    }
    return entry;
  }

  /** Sends a notification — no id, no reply expected. */
  private notify(method: string): void {
    const stdin = this.proc?.stdin;
    if (!stdin || stdin.destroyed || this.exited) {
      return;
    }
    stdin.write(Framer.encode(JSON.stringify({ jsonrpc: '2.0', method })));
  }

  private receive(chunk: Buffer): void {
    let bodies: string[];
    try {
      bodies = this.framer.push(chunk);
    } catch (err) {
      // The stream position is no longer known. Refuse rather than guess.
      const e = err instanceof CoreError ? err : new CoreError('protocol', String(err));
      this.opts.log(`framing: ${e.message}`);
      this.rejectAll(e);
      this.kill();
      return;
    }
    for (const body of bodies) {
      let msg: {
        id?: number | string | null;
        result?: unknown;
        error?: { code: number; message: string; data?: RpcErrorData };
      };
      try {
        msg = JSON.parse(body);
      } catch {
        this.opts.log('the core sent a frame that is not JSON');
        continue;
      }
      if (msg.id === undefined || msg.id === null) {
        continue; // a server-initiated notification; none are defined yet
      }
      const id = typeof msg.id === 'string' ? Number.parseInt(msg.id, 10) : msg.id;
      const pending = this.settle(id);
      if (!pending) {
        continue;
      }
      if (msg.error) {
        pending.reject(new RpcError(msg.error.code, msg.error.message, msg.error.data));
      } else {
        pending.resolve(msg.result);
      }
    }
  }

  private rejectAll(err: Error): void {
    for (const [, pending] of this.pending) {
      if (pending.timer !== undefined) {
        clearTimeout(pending.timer);
      }
      pending.reject(err);
    }
    this.pending.clear();
  }

  /**
   * Ends the process. SIGTERM first, then SIGKILL if it is ignored — a core
   * that traps SIGTERM must not outlive the window that spawned it.
   */
  private kill(): void {
    this.exited = true;
    const proc = this.proc;
    if (!proc) {
      return;
    }
    proc.stdin?.end();
    proc.kill();
    const t = setTimeout(() => {
      if (proc.exitCode === null && proc.signalCode === null) {
        proc.kill('SIGKILL');
      }
    }, KILL_GRACE_MS);
    // Never hold the extension host open on this timer.
    t.unref?.();
    proc.once('exit', () => clearTimeout(t));
  }

  /**
   * Shuts the core down: the LSP handshake in reverse, then a kill if it does
   * not go. A leaked subprocess outlives the window that spawned it.
   */
  dispose(): void {
    if (this.disposed) {
      return;
    }
    this.disposed = true;
    const proc = this.proc;
    if (!proc || this.exited) {
      return;
    }
    void this.request('shutdown', {}).catch(() => undefined);
    this.notify('exit');
    const t = setTimeout(() => {
      if (proc.exitCode === null && proc.signalCode === null) {
        // SIGTERM, then SIGKILL if that is ignored.
        this.kill();
      }
    }, 500);
    t.unref?.();
    proc.once('exit', () => clearTimeout(t));
  }
}

/** One line naming how the process ended, for a banner. */
export function describeExit(info: ExitInfo): string {
  const how =
    info.signal !== null
      ? `killed by ${info.signal}`
      : info.code === null
        ? 'exited for an unknown reason'
        : `exit code ${info.code}`;
  return info.stderr ? `${how}\n${info.stderr}` : how;
}
