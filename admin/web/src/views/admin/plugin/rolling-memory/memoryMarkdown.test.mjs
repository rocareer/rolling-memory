// memoryMarkdown 自动格式化的契约直测：JSON/JSONL/源码/散文必须分派到正确的呈现形态，
// 且格式化只做展示层形变、绝不改写金库字节。
// 运行：node --test admin/web/src/views/admin/plugin/rolling-memory/memoryMarkdown.test.mjs
import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const ts = require('typescript')

const source = readFileSync(new URL('./memoryMarkdown.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText
const moduleShim = { exports: {} }
new Function('exports', 'module', 'require', compiled)(moduleShim.exports, moduleShim, require)
const { toMemoryMarkdown, splitChunkHeader, chunkAbsoluteLineStart } = moduleShim.exports

test('JSON value is pretty-printed inside a json fence', () => {
    const out = toMemoryMarkdown('{"a":1,"b":[true,null]}')
    assert.ok(out.startsWith('```json\n') && out.endsWith('\n```'), `unexpected fence: ${out}`)
    assert.ok(out.includes('\n  "a": 1'), 'indentation must be 2 spaces')
})

test('oversized JSON is fenced verbatim without expansion', () => {
    const huge = '{"pad":"' + 'x'.repeat(262144) + '"}'
    const out = toMemoryMarkdown(huge)
    assert.ok(out.startsWith('```json\n'), 'still fenced as json')
    assert.ok(out.includes(huge), 'verbatim payload must survive intact')
    assert.ok(!out.includes('\n  "pad"'), 'no pretty expansion beyond the size guard')
})

test('JSONL keeps one record per line inside a fence', () => {
    const text = '{"role":"user","text":"hi"}\n{"role":"assistant","text":"yo"}'
    const out = toMemoryMarkdown(text)
    assert.ok(out.startsWith('```json\n'), `expected json fence, got: ${out.slice(0, 20)}`)
    assert.ok(out.includes(text), 'line structure must be preserved byte-for-byte')
})

test('noetic prose passes through untouched', () => {
    const text = '# Answer\n\nSee [docs](https://example.com) for details.'
    assert.equal(toMemoryMarkdown(text, { layer: 'noetic' }), text)
})

test('markdown chunks pass through, source chunks get a language fence', () => {
    const md = '## Notes\n\n- item one'
    assert.equal(toMemoryMarkdown(md, { relPath: 'docs/notes.md' }), md)
    const go = 'package main\n\nfunc main() {}\n'
    const out = toMemoryMarkdown(go, { relPath: 'cmd/app/main.go' })
    assert.ok(out.startsWith('```go\n') && out.includes(go), `expected go fence: ${out}`)
})

test('background chunks without JSON shape are fenced as plaintext', () => {
    const text = 'line one\nline two\nline three'
    const out = toMemoryMarkdown(text, { layer: 'background' })
    assert.ok(out.startsWith('```plaintext\n') && out.includes(text), `expected plaintext fence: ${out}`)
})

test('partial/illegal JSON with a .json source still fences under the extension', () => {
    const text = '{"cut":"mid-file…'
    const out = toMemoryMarkdown(text, { relPath: 'data/big.json' })
    assert.ok(out.startsWith('```json\n') && out.includes(text), `expected json fence: ${out}`)
})

test('fence width grows past the longest backtick run in the payload', () => {
    const text = 'before\n```\ninner\n```\nafter'
    const out = toMemoryMarkdown(text, { layer: 'background' })
    assert.ok(out.startsWith('````plaintext\n'), `fence must widen to 4 ticks: ${out.slice(0, 15)}`)
    assert.ok(out.endsWith('\n````'), 'closing fence must match')
    assert.ok(out.includes(text), 'payload survives verbatim')
})

test('hint-free prose passes through and empty input stays empty', () => {
    const prose = 'Q: 今天做了啥？\nA: 收口了溯源弹窗。'
    assert.equal(toMemoryMarkdown(prose), prose)
    assert.equal(toMemoryMarkdown(''), '')
    assert.equal(toMemoryMarkdown('   \n  '), '   \n  ')
})

test('chunk coordinate is hoisted out of the fence and exposes its start line', () => {
    const text = '[core/x.go#L810-850] (/home/user/proj/core/x.go)\npackage main\n\nfunc main() {}'
    const out = toMemoryMarkdown(text, { layer: 'background', relPath: 'core/x.go' })
    assert.ok(
        out.startsWith('`[core/x.go#L810-850] (/home/user/proj/core/x.go)`\n\n```go\n'),
        `coordinate must lead outside the fence: ${out.slice(0, 90)}`
    )
    assert.ok(!out.includes('```go\n[core/x.go'), 'coordinate must not stay inside the fence')
    const info = splitChunkHeader(text)
    assert.equal(info.lineStart, 810)
    assert.equal(info.rest, 'package main\n\nfunc main() {}')
})

test('blank lines inside a chunk body survive so renderer row numbers stay 1:1 with the file', () => {
    const text = '[a/b.md#L3-6] (/abs/b.md)\nline3\n\nline5\n\n'
    const out = toMemoryMarkdown(text, { layer: 'background', relPath: 'a/b.md' })
    assert.ok(out.includes('line3\n\nline5\n\n'), `blank lines must survive: ${out}`)
})

test('chunk bodies stay verbatim inside the fence (no JSON pretty-print reflow)', () => {
    const text = '[data/x.json#L1-3] (/abs/x.json)\n{"a":1}\n\n{"b":2}'
    const out = toMemoryMarkdown(text, { layer: 'background', relPath: 'data/x.json' })
    assert.ok(out.startsWith('`[data/x.json#L1-3] (/abs/x.json)`\n\n```json\n'), `verbatim json fence: ${out.slice(0, 80)}`)
    assert.ok(out.includes('{"a":1}\n\n{"b":2}'), 'payload must not be reformatted')
})

test('absolute line numbering only applies when the body is 1:1 with the anchor range', () => {
    assert.equal(chunkAbsoluteLineStart('[a/x.go#L10-12] (/abs/x.go)\nl10\n\nl12'), 10)
    // 旧世代正文丢过空行：行数对不上区间时必须回退相对行号，绝不显示会漂移的假绝对行号。
    assert.equal(chunkAbsoluteLineStart('[a/x.go#L10-40] (/abs/x.go)\nl10\nl11'), 0)
    assert.equal(chunkAbsoluteLineStart('[a/x.pdf#page2] (/abs/x.pdf)\nbody'), 0)
    assert.equal(chunkAbsoluteLineStart('plain body without a header'), 0)
})

test('non-line anchors hoist without a row-number offset', () => {
    const text = '[report.pdf#page2] (/abs/report.pdf)\nbody text'
    assert.equal(splitChunkHeader(text).lineStart, 0)
    const out = toMemoryMarkdown(text, { layer: 'background', relPath: 'report.pdf' })
    assert.ok(out.startsWith('`[report.pdf#page2] (/abs/report.pdf)`'), `expected hoisted header: ${out}`)
})

test('bodies without a chunk header are untouched and never parse as coordinates', () => {
    assert.equal(splitChunkHeader('line one\nline two'), null)
    assert.equal(splitChunkHeader('[user] 继续'), null)
    assert.equal(splitChunkHeader(''), null)
})
