// One core process per window, supervised.
//
// The supervisor exists so that failure is a state the panel can render rather
// than an exception nobody catches. It never restarts the core by itself: a
// crash loop that silently respawns is how a broken binary looks healthy in a
// log. One clear message, one explicit retry — that is the whole policy.

import * as vscode from 'vscode';
import { CoreError, ComposureCore, describeExit, resolveBinaryPath, type ExitInfo } from './core';
import type { Failure } from '../shared/protocol';

export class CoreSession implements vscode.Disposable {
  private core: ComposureCore | undefined;
  private starting: Promise<ComposureCore> | undefined;
  /**
   * Bumped by restart() and dispose(). A start that was already in flight when
   * one of those ran belongs to a previous generation: it must not install
   * itself as the current core, and it must dispose the process it spawned.
   */
  private generation = 0;

  constructor(
    private readonly extensionPath: string,
    private readonly output: vscode.OutputChannel,
    /** Called when a running core dies of its own accord, never on dispose(). */
    private readonly onCrash: (failure: Failure) => void,
  ) {}

  /** The path this session would spawn, and the platform it decided that from. */
  binary(): { path: string; target: string; configured: boolean } {
    const configured = vscode.workspace.getConfiguration('composure').get<string>('corePath', '');
    return resolveBinaryPath(this.extensionPath, configured);
  }

  /** Starts the core if it is not already running. Concurrent callers share one start. */
  async ensure(): Promise<ComposureCore> {
    if (this.core?.running) {
      return this.core;
    }
    if (this.starting) {
      return this.starting;
    }
    const { path: binaryPath } = this.binary();
    const generation = this.generation;
    const core = new ComposureCore({
      binaryPath,
      log: (line) => this.output.appendLine(line),
      onExit: (info) => {
        if (generation === this.generation) {
          this.handleExit(info);
        }
      },
    });
    const starting = core.start().then(() => {
      if (generation !== this.generation) {
        // restart() or dispose() ran while this core was starting. It is
        // already superseded; do not hand it out and do not leak it.
        core.dispose();
        throw new CoreError('core-crashed', 'the Composure core was replaced while starting', '');
      }
      this.core = core;
      return core;
    });
    this.starting = starting;
    void starting.catch(() => undefined).finally(() => {
      if (this.starting === starting) {
        this.starting = undefined;
      }
    });
    return starting;
  }

  /**
   * Sends a request, starting the core first if needed.
   *
   * `timeoutMs` overrides the client's default bound for ONE call. It exists
   * for Epic 8's image lookups, which are optional decoration on an already
   * drawn pane: they must give up sooner than the requests a pane cannot be
   * drawn without, so a dead network costs a fact rather than a request slot.
   */
  async request<T>(method: string, params: unknown, timeoutMs?: number): Promise<T> {
    const core = await this.ensure();
    return core.request<T>(method, params, timeoutMs);
  }

  /**
   * Drops the current core so the next ensure() spawns a fresh one.
   *
   * A start that is still in flight is dropped too. Leaving it would let a
   * retry pressed during startup resolve to the very core this call disposed —
   * the reader asks for a fresh process and gets the dead one back.
   */
  restart(): void {
    this.generation++;
    this.core?.dispose();
    this.core = undefined;
    // The in-flight start disposes its own core when it sees the generation
    // moved; dropping the promise here is what stops a retry from resolving to
    // it in the meantime.
    this.starting = undefined;
  }

  private handleExit(info: ExitInfo): void {
    this.core = undefined;
    this.output.appendLine(`core exited: ${describeExit(info)}`);
    this.onCrash({
      kind: 'core-crashed',
      title: 'The Composure core stopped',
      detail: describeExit(info),
    });
  }

  dispose(): void {
    this.generation++;
    this.core?.dispose();
    this.core = undefined;
    this.starting = undefined;
  }
}

/** Turns any thrown value into a named, retryable failure. Nothing reaches a reader untyped. */
export function toFailure(err: unknown, binary: { path: string; target: string }): Failure {
  if (err instanceof CoreError) {
    switch (err.kind) {
      case 'core-missing':
        return {
          kind: 'core-missing',
          title: 'No Composure core for this platform',
          detail: `expected: ${err.detail}\nplatform: ${binary.target}`,
        };
      case 'spawn-failed':
        return { kind: 'spawn-failed', title: 'The Composure core did not start', detail: err.detail || err.message };
      case 'core-crashed':
        return { kind: 'core-crashed', title: 'The Composure core stopped', detail: err.detail || err.message };
      case 'timeout':
        return { kind: 'timeout', title: 'The Composure core stopped answering', detail: err.detail || err.message };
      case 'protocol':
        return { kind: 'spawn-failed', title: 'The Composure core is the wrong version', detail: err.detail || err.message };
    }
  }
  return {
    kind: 'internal',
    title: 'Composure could not draw this stack',
    detail: err instanceof Error ? err.message : String(err),
  };
}
