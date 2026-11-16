// 记忆正文自动格式化：把金库里的异构正文（散文 / JSON / JSONL / 源码）转换为可直接渲染的
// Markdown 文本，交由 md-editor-v3 的 MdPreview 统一呈现。本模块只做展示层形变、不触碰金库字节：
// 散文与既有 Markdown 原样透传；结构化内容包进代码围栏（围栏长度按正文反引号游程取安全值）；
// 「复制原文」始终复制未形变的原始字节。

/** 正文形态线索：层级来自单元坐标，relPath 来自分块来源文件；两者都可能缺省。 */
export interface MemoryContentHint {
    layer?: string
    relPath?: string
}

/** 分块坐标首行（`[rel_path#anchor] (abs_path)`）的解析结果；lineStart 为 0 表示锚点不是行区间。 */
export interface MemoryChunkHeader {
    header: string
    rest: string
    lineStart: number
    lineEnd: number
}

const CHUNK_HEADER_RE = /^\s*\[[^\]\n]+#[^\]\n]+\]\s*(?:\([^)\n]*\))?\s*$/
const CHUNK_HEADER_LINE_RE = /#L(\d+)-(\d+)\]/

/**
 * 识别正文首行的分块坐标（背景分块正文的头部契约）。命中时返回头部行、余下正文与真实行区间，
 * 供渲染层把坐标提升为行首标题、并把代码行号校准到源文件行号；未命中返回 null。
 */
export function splitChunkHeader(text: string): MemoryChunkHeader | null {
    const body = String(text ?? '')
    const cut = body.indexOf('\n')
    const first = cut >= 0 ? body.slice(0, cut) : body
    if (!CHUNK_HEADER_RE.test(first)) return null
    const matched = CHUNK_HEADER_LINE_RE.exec(first)
    return {
        header: first.trim(),
        rest: cut >= 0 ? body.slice(cut + 1) : '',
        lineStart: matched ? Number(matched[1]) : 0,
        lineEnd: matched ? Number(matched[2]) : 0,
    }
}

/**
 * 分块正文可校准的绝对起始行：仅当正文行数与锚点区间逐行相等（1:1，正文未丢空行也未重排）时
 * 返回起始行；旧世代正文（空行被丢弃）或非行区间锚点返回 0，渲染层据此回退相对行号。
 */
export function chunkAbsoluteLineStart(text: string): number {
    const info = splitChunkHeader(text)
    if (!info || info.lineStart <= 1 || info.lineEnd < info.lineStart) return 0
    const bodyLines = info.rest.split('\n').length
    return bodyLines === info.lineEnd - info.lineStart + 1 ? info.lineStart : 0
}

/** 单个 JSON 值缩进展开的长度上限：超长文档仍围栏呈现，但不展开缩进，避免渲染卡顿。 */
const PRETTY_JSON_MAX = 262144

/**
 * 扩展名 → highlight.js 语言标签（lib/common 已注册集内）。
 * 显式标注可避免 MdPreview 对无标签围栏做昂贵的自动探测；未列出的扩展名回落 plaintext。
 */
const EXT_LANGUAGE: Record<string, string> = {
    md: 'markdown',
    markdown: 'markdown',
    mdx: 'markdown',
    json: 'json',
    jsonl: 'json',
    ndjson: 'json',
    go: 'go',
    py: 'python',
    js: 'javascript',
    mjs: 'javascript',
    cjs: 'javascript',
    ts: 'typescript',
    tsx: 'typescript',
    jsx: 'javascript',
    html: 'xml',
    htm: 'xml',
    xml: 'xml',
    svg: 'xml',
    vue: 'xml',
    css: 'css',
    scss: 'scss',
    less: 'less',
    sh: 'bash',
    bash: 'bash',
    zsh: 'bash',
    c: 'c',
    h: 'c',
    cc: 'cpp',
    cpp: 'cpp',
    cxx: 'cpp',
    hpp: 'cpp',
    cs: 'csharp',
    java: 'java',
    kt: 'kotlin',
    kts: 'kotlin',
    rs: 'rust',
    rb: 'ruby',
    php: 'php',
    swift: 'swift',
    lua: 'lua',
    r: 'r',
    pl: 'perl',
    sql: 'sql',
    yml: 'yaml',
    yaml: 'yaml',
    toml: 'ini',
    ini: 'ini',
    conf: 'ini',
    cfg: 'ini',
    properties: 'ini',
    diff: 'diff',
    patch: 'diff',
    graphql: 'graphql',
    gql: 'graphql',
    wasm: 'wasm',
    wat: 'wasm',
    vb: 'vbnet',
    txt: 'plaintext',
    text: 'plaintext',
    log: 'plaintext',
    out: 'plaintext',
    csv: 'plaintext',
    tsv: 'plaintext',
}

const extensionOf = (relPath: string): string => {
    const matched = /\.([A-Za-z0-9]+)$/.exec(String(relPath || ''))
    return matched ? matched[1].toLowerCase() : ''
}

/** 取正文中反引号游程的安全围栏宽度：始终比正文里最长的连续反引号多一个。 */
const fenceBlock = (body: string, language: string): string => {
    let width = 3
    const runs = body.match(/`+/g)
    if (runs) {
        for (const run of runs) {
            width = Math.max(width, run.length + 1)
        }
    }
    const bar = '`'.repeat(width)
    return bar + language + '\n' + body + '\n' + bar
}

const parseJSONText = (text: string): { parsed: boolean; value?: unknown } => {
    try {
        return { parsed: true, value: JSON.parse(text) }
    } catch {
        return { parsed: false }
    }
}

const isJSONRecord = (line: string): boolean => {
    try {
        JSON.parse(line)
        return true
    } catch {
        return false
    }
}

/**
 * 把单条记忆正文转换为可渲染的 Markdown 文本。
 * 分派次序（前者命中即返回）：分块坐标首行提升 → 既有 Markdown 透传 → 单个 JSON 值缩进围栏 →
 * JSONL 逐行围栏 → 已知扩展名按语言围栏 → 行结构化底物按纯文本围栏 → 其余散文透传。
 */
export function toMemoryMarkdown(text: string, hint?: MemoryContentHint): string {
    const body = String(text ?? '')
    if (!body.trim()) return body
    // 分块坐标首行提升为围栏之外的行首标题；围栏内正文原样呈现（不做 JSON 缩进 / JSONL 或语言
    // 探测类重排），渲染行数才能与锚点区间逐行对应、行号才能校准到源文件真实行号。
    const chunk = splitChunkHeader(body)
    if (chunk) {
        const head = chunk.header.includes('`') ? chunk.header : '`' + chunk.header + '`'
        if (!chunk.rest.trim()) return head
        const sourceLanguage = EXT_LANGUAGE[extensionOf(String(hint?.relPath || ''))] || 'plaintext'
        return head + '\n\n' + fenceBlock(chunk.rest, sourceLanguage)
    }
    const layer = String(hint?.layer || '').toLowerCase()
    const language = EXT_LANGUAGE[extensionOf(String(hint?.relPath || ''))] || ''
    // 结晶答案与 .md 文档本身就是 Markdown，原样透传。
    if (layer === 'noetic' || language === 'markdown') return body
    const trimmed = body.trim()
    // 单个 JSON 值：合法则缩进展开后围栏；超长仅围栏不展开。
    if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
        const attempt = parseJSONText(trimmed)
        if (attempt.parsed) {
            const pretty = trimmed.length <= PRETTY_JSON_MAX ? JSON.stringify(attempt.value, null, 2) : trimmed
            return fenceBlock(pretty, 'json')
        }
    }
    // JSONL：每一行都是合法 JSON 记录时整段围栏（保留逐行结构）。
    const records = trimmed.split(/\r?\n/).filter((line) => line.trim() !== '')
    if (records.length > 0 && records.every(isJSONRecord)) return fenceBlock(trimmed, 'json')
    // 已知来源扩展名：按语言围栏（部分/非法 JSON 也由扩展名兜底）。
    if (language) return fenceBlock(body, language)
    // 行结构化底物（背景分块正文 / 会话记录）：无语言线索时按纯文本围栏，保留全部换行。
    if (layer === 'background' || layer === 'episodic') return fenceBlock(body, 'plaintext')
    // 其余散文原样透传（渲染器的软换行保持逐行阅读）。
    return body
}
