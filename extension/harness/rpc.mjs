// Minimal LSP-framed JSON-RPC client for the shipped `composure serve` core.
// Same wire the extension host speaks (extension/host/core.ts), reduced to the
// half a screenshot harness needs: spawn, initialize, request, shutdown.
import { spawn } from 'node:child_process';

export function open(bin) {
  const proc = spawn(bin, ['serve'], { stdio: ['pipe', 'pipe', 'pipe'] });
  let buf = Buffer.alloc(0);
  const waiting = new Map();
  let nextId = 1;
  proc.stderr.on('data', (d) => process.stderr.write(`[core] ${d}`));
  proc.stdout.on('data', (chunk) => {
    buf = Buffer.concat([buf, chunk]);
    for (;;) {
      const head = buf.indexOf('\r\n\r\n');
      if (head < 0) return;
      const m = /Content-Length:\s*(\d+)/i.exec(buf.subarray(0, head).toString('ascii'));
      if (!m) throw new Error('no Content-Length');
      const len = Number(m[1]);
      const start = head + 4;
      if (buf.length < start + len) return;
      const body = JSON.parse(buf.subarray(start, start + len).toString('utf8'));
      buf = buf.subarray(start + len);
      const pending = waiting.get(body.id);
      if (!pending) continue;
      waiting.delete(body.id);
      if (body.error) pending.reject(new Error(`${body.error.code}: ${body.error.message}`));
      else pending.resolve(body.result);
    }
  });
  const request = (method, params) =>
    new Promise((resolve, reject) => {
      const id = nextId++;
      waiting.set(id, { resolve, reject });
      const payload = Buffer.from(JSON.stringify({ jsonrpc: '2.0', id, method, params }), 'utf8');
      proc.stdin.write(`Content-Length: ${payload.length}\r\n\r\n`);
      proc.stdin.write(payload);
      setTimeout(() => { if (waiting.delete(id)) reject(new Error(`${method} timed out`)); }, 30000);
    });
  return { request, close: () => proc.kill() };
}
