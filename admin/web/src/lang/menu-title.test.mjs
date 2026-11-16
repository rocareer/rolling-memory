import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { test } from 'node:test'

const require = createRequire(import.meta.url)
const ts = require('typescript')
const source = readFileSync(new URL('./index.ts', import.meta.url), 'utf8')
const start = source.indexOf('const menuTitleDict:')
const end = source.indexOf('\nexport default i18n', start)
assert.ok(start >= 0 && end > start)
const javascript = ts.transpileModule(source.slice(start, end).replace('export function translateMenuTitle', 'function translateMenuTitle'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText
const i18n = {
    global: {
        locale: { value: 'zh-cn' },
        te: (key) => key === 'custom.memory',
        t: (key) => ({ 'custom.memory': 'Translated custom', 'layouts.untitled': 'Untitled' })[key],
    },
}
const translateMenuTitle = new Function('i18n', `${javascript}; return translateMenuTitle`)(i18n)
const browser = (title, rawTitle) => ({ name: 'plugin/rolling-memory', meta: { title, rawTitle } })

test('factory memory browser titles follow the active language', () => {
    for (const [locale, expected] of [
        ['zh-cn', '记忆浏览'],
        ['en', 'Memory Browser'],
    ]) {
        i18n.global.locale.value = locale
        for (const title of ['记忆管理', '记忆浏览', '', undefined]) {
            assert.equal(translateMenuTitle(browser(title)), expected)
        }
    }
})

test('custom memory browser titles survive language changes and tab translation', () => {
    for (const locale of ['zh-cn', 'en']) {
        i18n.global.locale.value = locale
        assert.equal(translateMenuTitle(browser('我的记忆')), '我的记忆')
        assert.equal(translateMenuTitle(browser('custom.memory')), 'Translated custom')
        assert.equal(translateMenuTitle(browser('Translated custom', 'custom.memory')), 'Translated custom')
        assert.equal(translateMenuTitle(browser('changed title', '我的记忆')), '我的记忆')
    }
})

test('other menu titles translate factory defaults and preserve custom titles', () => {
    i18n.global.locale.value = 'zh-cn'
    assert.equal(translateMenuTitle({ name: 'dashboard', meta: { title: '控制台' } }), '控制台')
    assert.equal(translateMenuTitle({ name: 'dashboard', meta: { title: '自定义控制台' } }), '自定义控制台')
    assert.equal(translateMenuTitle({ name: 'other', meta: { title: 'custom.memory' } }), 'Translated custom')
    assert.equal(translateMenuTitle({ name: 'other', meta: { title: '自定义' } }), '自定义')
    assert.equal(translateMenuTitle({ name: 'other' }), 'Untitled')
    i18n.global.locale.value = 'en'
    assert.equal(translateMenuTitle({ name: 'dashboard', meta: { title: '控制台' } }), 'Dashboard')
    assert.equal(translateMenuTitle({ name: 'dashboard', meta: { title: '自定义控制台' } }), '自定义控制台')
})
