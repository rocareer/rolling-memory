import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { createRequire } from 'node:module'
import { parse, compileScript } from 'vue/compiler-sfc'

const require = createRequire(import.meta.url)
const ts = require('typescript')
const source = readFileSync(new URL('./eval.vue', import.meta.url), 'utf8')
const { descriptor, errors } = parse(source, { filename: 'eval.vue' })
assert.deepEqual(errors, [])
compileScript(descriptor, { id: 'eval-test' })

const script = descriptor.scriptSetup.content
const between = (start, end) => {
    const first = script.indexOf(start)
    const last = script.indexOf(end, first)
    assert.ok(first >= 0 && last > first, `missing ${start}`)
    return script.slice(first, last)
}
const javascript = (text) => ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
const scopeSource = between('const evalScopeMatches =', '\nconst expectedEvalScope')
const readySource = between('const evalCorpusReady =', '\nconst readinessTitle')
const labelSource = between('const evalArmLabel =', '\nwatch(() => evalConfig.arm_mode')
const suiteSource = between('const onSuiteChange =', '\nconst onTierChange')
const tierLimitSource = between('const batchLimit =', '\nconst activeTab')
const routeSource = between('const applyRouteQuery =', '\nonMounted')
const detailSource = between('let detailRequestID =', '\nlet compareRequestID =')
const archiveSource = between('const includeArchivedTiers =', '\nconst archivedTierCount')
const overviewSource = between('let overviewRequestID =', '\nconst saveAIConfig')
const computed = (fn) => ({ get value() { return fn() } })
const scopeMatches = new Function(`${javascript(scopeSource)}; return evalScopeMatches`)()
const ready = new Function('evalManifest', 'overview', 'evalConfig', 'query', 'evalScopeMatches', 'computed', `${javascript(readySource)}; return evalCorpusReady`)
const label = new Function('evalConfig', 'isEn', 'computed', `${javascript(labelSource)}; return evalArmLabel`)

const modes = [
    { arm: 'sdk-local-plain', mode: 'local_plain', suffix: 'local-plain', transport: 'ramesdk.local_plain.offline-v1', endpoint: 'local://sqlite-fts5-bm25' },
    { arm: 'sdk-pulse-plain', mode: 'pulse_plain', suffix: 'pulse-plain', transport: 'ramesdk.pulse_plain.open-memory-v1', endpoint: 'https://example.test/open/memory/v1' },
    { arm: 'sdk-pulse-cipher', mode: 'pulse_cipher', suffix: 'pulse-cipher', transport: 'ramesdk.pulse_cipher.open-memory-v1', endpoint: 'https://example.test/open/memory/v1' },
]

test('title tracks each selected arm in both languages', () => {
    for (const mode of modes) {
        for (const en of [false, true]) {
            assert.match(label({ arm_mode: mode.arm }, { value: en }, computed).value, new RegExp(mode.mode))
        }
    }
    assert.match(label({ arm_mode: 'oracle' }, { value: false }, computed).value, /Oracle/)
    assert.match(descriptor.template.content, /evalArmLabel/)
    assert.doesNotMatch(descriptor.template.content, /SDK Cloud Evaluation Lab/)
})

test('manifest readiness requires matching suite, mode, scope, transport and endpoint', () => {
    for (const suite of ['locomo', 'atm']) {
        for (const mode of modes) {
            const scope = `u10-mem-eval-${suite === 'atm' ? 'atmbench' : 'locomo'}-${mode.suffix}-generation-1`
            const manifest = { suite, scope, transport: mode.transport, endpoint: mode.endpoint, user_id: '10', local_fallback: false, documents: 2, corpus_sha256: 'digest' }
            const config = { arm_mode: mode.arm, pulse_mode: mode.mode }
            const overview = { value: { readiness: { ready: true } } }
            const check = (value = manifest, selected = config, state = overview) => ready({ value }, state, selected, { suite }, scopeMatches, computed).value
            assert.equal(check(), true, `${suite} ${mode.mode}`)
            assert.equal(check({ ...manifest, suite: suite === 'atm' ? 'locomo' : 'atm' }), false)
            assert.equal(check({ ...manifest, scope: scope.replace(mode.suffix, modes[(modes.indexOf(mode) + 1) % modes.length].suffix) }), false)
            assert.equal(check({ ...manifest, scope: `${scope}:other` }), false)
            assert.equal(check({ ...manifest, scope: scope.replace(`-${mode.suffix}`, '') }), false)
            assert.equal(check({ ...manifest, transport: modes[(modes.indexOf(mode) + 1) % modes.length].transport }), false)
            assert.equal(check({ ...manifest, endpoint: mode.mode === 'local_plain' ? modes[1].endpoint : modes[0].endpoint }), false)
            assert.equal(check({ ...manifest, local_fallback: true }), false)
            assert.equal(check(manifest, config, { value: { readiness: { ready: false } } }), false)
            assert.equal(check(manifest, { ...config, arm_mode: 'oracle' }), false)
            assert.equal(check(manifest, { ...config, arm_mode: modes[(modes.indexOf(mode) + 1) % modes.length].arm }), false)
        }
    }
})

test('out-of-order overview responses cannot replace the current context', async () => {
    const pending = []
    const request = (options) => new Promise((resolve, reject) => pending.push({ options, resolve, reject }))
    const overview = { value: {} }
    const loading = { value: false }
    const overviewError = { value: '' }
    const query = { suite: 'locomo' }
    const config = { arm_mode: 'sdk-local-plain', pulse_mode: 'local_plain' }
    const create = new Function('request', 'overview', 'loading', 'overviewLoadError', 'query', 'evalConfig', 'isLocalMode', 'selectedRecord', 'detailRunId', 'detailItems', 'detailAvailable', 'detailReason', 'isEn', 'loadRunDetailList', `${javascript(overviewSource)}; return loadOverview`)
    const load = create(request, overview, loading, overviewError, query, config, { value: false }, { value: null }, { value: '' }, { value: [] }, { value: true }, { value: '' }, { value: true }, () => {})
    const first = load()
    query.suite = 'atm'
    const second = load()
    pending[1].resolve({ data: { data: { suite: 'atm' } } })
    await second
    pending[0].resolve({ data: { data: { suite: 'locomo' } } })
    await first
    assert.equal(overview.value.suite, 'atm')
    assert.equal(loading.value, false)
    assert.equal(pending[0].options.data.suite, 'locomo')
    assert.equal(pending[1].options.data.suite, 'atm')
    assert.equal(pending[0].options.timeout, 45000)
    const failed = load()
    pending[2].reject(new Error('probe timeout'))
    await failed
    assert.equal(overviewError.value, 'probe timeout')
    assert.equal(loading.value, false)
})

test('suite switching preserves the selected arm and resets suite-scoped results', () => {
    const run = new Function(
        'overview', 'overviewLoadError', 'query', 'evalConfig', 'selectedRecord', 'detailRunId', 'detailItems',
        'compareRunId', 'compareItems', 'compareAvailable', 'compareReason',
        'corpusModalVisible', 'probeTopK', 'loadOverview',
        `${javascript(suiteSource)}; return onSuiteChange`
    )
    for (const arm of [...modes.map((mode) => mode.arm), 'oracle']) {
        for (const suite of ['locomo', 'atm']) {
            const query = { suite, page: 2, category: '1' }
            const config = { arm_mode: arm, category: '1' }
            const overview = { value: { manifest: {}, readiness: {}, store_path: 'stale' } }
            const compareRunId = { value: 'previous-suite-run' }
            const compareItems = { value: [{ id: 'stale' }] }
            const compareAvailable = { value: false }
            const compareReason = { value: 'stale' }
            let loads = 0
            run(
                overview, { value: '' }, query, config, { value: null }, { value: '' }, { value: [] },
                compareRunId, compareItems, compareAvailable, compareReason,
                { value: true }, { value: 0 }, () => { loads++ }
            )()
            assert.equal(config.arm_mode, arm)
            assert.equal(query.tier, suite === 'atm' ? 'hard' : '1540')
            assert.equal(compareRunId.value, '')
            assert.deepEqual(compareItems.value, [])
            assert.equal(compareAvailable.value, true)
            assert.equal(compareReason.value, '')
            assert.equal(overview.value.manifest, null)
            assert.deepEqual(overview.value.run_history, [])
            assert.deepEqual(overview.value.baselines, [])
            assert.equal(loads, 1)
        }
    }
})

test('only currently offered tiers are visible and route limits share one derivation', () => {
    assert.match(descriptor.template.content, /value="1540"/)
    assert.doesNotMatch(descriptor.template.content, /value="100"/)
    assert.doesNotMatch(descriptor.template.content, /value="200"/)
    assert.match(descriptor.template.content, /name="run"/)
    assert.match(descriptor.template.content, /name="results"/)
    assert.match(descriptor.template.content, /name="questions"/)
    assert.match(descriptor.template.content, /name="protocol"/)

    const tierLimit = new Function('query', 'computed', `${javascript(tierLimitSource)}; return batchLimit`)
    for (const [suite, tier, want] of [
        ['locomo', '1540', 1540],
        ['atm', 'hard', 31],
        ['atm', '1013', 1013],
    ]) {
        assert.equal(tierLimit({ suite, tier }, computed).value, want, `${suite}/${tier}`)
    }

    const applyRoute = new Function('router', 'query', 'evalConfig', 'probeTopK', `${javascript(routeSource)}; return applyRouteQuery`)
    const query = { suite: 'locomo', tier: '1540' }
    const config = { top_k: 20 }
    const probeTopK = { value: 20 }
    applyRoute({ currentRoute: { value: { query: { suite: 'locomo', tier: '1540' } } } }, query, config, probeTopK)()
    assert.equal(query.tier, '1540')
    assert.equal(tierLimit(query, computed).value, 1540)
    // 不再提供的便捷档 100/200 走同一路由：归一到官方 1540。
    applyRoute({ currentRoute: { value: { query: { suite: 'locomo', tier: '100' } } } }, query, config, probeTopK)()
    assert.equal(query.tier, '1540')
    applyRoute({ currentRoute: { value: { query: { suite: 'locomo', tier: '200' } } } }, query, config, probeTopK)()
    assert.equal(query.tier, '1540')
})

test('out-of-order detail responses cannot replace the newly selected run', async () => {
    const pending = []
    const request = (options) => new Promise((resolve, reject) => pending.push({ options, resolve, reject }))
    const detailItems = { value: [] }
    const detailAvailable = { value: true }
    const detailReason = { value: '' }
    const loadingDetails = { value: false }
    const create = new Function(
        'request', 'detailItems', 'detailAvailable', 'detailReason', 'loadingDetails', 'isEn',
        `${javascript(detailSource)}; return loadRunDetailList`
    )
    const load = create(request, detailItems, detailAvailable, detailReason, loadingDetails, { value: false })
    const first = load('run-a')
    const second = load('run-b')
    pending[1].resolve({ data: { data: { items: [{ id: 'b' }], available: true } } })
    await second
    pending[0].resolve({ data: { data: { items: [{ id: 'a' }], available: true } } })
    await first
    assert.deepEqual(detailItems.value, [{ id: 'b' }])
    assert.equal(loadingDetails.value, false)
    assert.equal(pending[0].options.data.run_id, 'run-a')
    assert.equal(pending[1].options.data.run_id, 'run-b')
})

test('stale suite records cannot leak into current suite results while LoCoMo 100 and 200 are both visible', () => {
    const ref = (value) => ({ value })
    const overview = { value: {
        run_history: [
            { id: 'atm-stale', suite: 'atm', tier: 'hard' },
            { id: 'locomo-100', suite: 'locomo', tier: '100' },
            { id: 'locomo-200', suite: 'locomo', tier: '200' },
        ],
        baselines: [
            { id: 'baseline-100', suite: 'locomo', tier: '100' },
            { id: 'baseline-200', suite: 'locomo', tier: '200' },
        ],
    } }
    const query = { suite: 'locomo' }
    const create = new Function('overview', 'query', 'ref', 'computed', `${javascript(archiveSource)}; return { visibleRunHistory, visibleBaselines, includeArchivedTiers }`)
    const state = create(overview, query, ref, computed)
    assert.deepEqual(state.visibleRunHistory.value.map((row) => row.id), ['locomo-100', 'locomo-200'])
    assert.deepEqual(state.visibleBaselines.value.map((row) => row.id), ['baseline-100', 'baseline-200'])
})
