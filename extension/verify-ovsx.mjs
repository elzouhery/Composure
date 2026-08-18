// Verify that every target `make publish-ovsx` uploaded is INSTALLABLE, not
// merely accepted.
//
// Open VSX can answer a publish with success and leave the version inactive and
// therefore invisible — it did exactly that on 0.1.0, accepting seven targets
// and serving one for roughly twenty-five minutes. A publish step that reports
// the CLI's exit code is reporting the wrong thing: the question is not "was it
// accepted" but "can somebody install it".
//
// Asked PER TARGET against /api/{ns}/{ext}/{target}/{version}, never against
// /api/-/query. The query endpoint is a search index and lags activation by an
// unbounded amount — it reported one target for a full 0.1.0 that was by then
// downloadable on all seven. An index is not the fact; the resolve endpoint is.
//
// Exits 0 when every expected target resolves and carries a download, 1
// otherwise, naming the gap. Reads extension/build/*.vsix for the expected set
// so it cannot drift from what was actually packaged.

import { readdirSync } from 'node:fs'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const { publisher, name, version } = require('./package.json')

const expected = readdirSync(new URL('build/', import.meta.url))
  .filter(f => f.endsWith('.vsix'))
  .map(f => f.replace(/^.*?-(?=(?:darwin|linux|alpine|win32|web|universal))/, '').replace(/\.vsix$/, ''))
  .sort()

if (expected.length === 0) {
  console.error('verify-ovsx: no packages in extension/build — nothing to verify')
  process.exit(1)
}

console.log(`  ${publisher}.${name} ${version} — ${expected.length} targets`)

const results = await Promise.all(expected.map(async target => {
  const url = `https://open-vsx.org/api/${publisher}/${name}/${target}/${version}`
  try {
    const res = await fetch(url, { headers: { accept: 'application/json' } })
    if (!res.ok) return { target, ok: false, why: `HTTP ${res.status}` }
    const body = await res.json()
    const download = body?.files?.download
    if (!download) return { target, ok: false, why: 'no download URL' }
    if (body.targetPlatform !== target) {
      return { target, ok: false, why: `served targetPlatform ${body.targetPlatform}` }
    }
    return { target, ok: true }
  } catch (e) {
    return { target, ok: false, why: e.message }
  }
}))

for (const r of results) {
  console.log(`  ${r.ok ? '✓' : '✗'} ${r.target}${r.ok ? '' : ` — ${r.why}`}`)
}

const missing = results.filter(r => !r.ok)
if (missing.length) {
  console.error('')
  console.error(`verify-ovsx: ${missing.length} of ${expected.length} targets are NOT installable.`)
  console.error('The upload was accepted; those platforms cannot install this version.')
  console.error('Activation has been observed to take ~25 minutes — re-run before reporting it.')
  console.error('If it persists, do NOT bump the version: the records exist. Ask at')
  console.error('https://github.com/EclipseFdn/open-vsx.org/issues')
  process.exit(1)
}

console.log('')
console.log(`verify-ovsx: all ${expected.length} targets installable.`)
