/**
 * 轻量级、零外部依赖的本项目 Markdown 富排版渲染器与 TOC（目录大纲）提取器。
 * 支持：
 *   - H1 / H2 / H3 / H4 标题自动生成确定性 slug 锚点与 TOC 树；
 *   - 围栏代码块（```lang ... ```）带语言顶栏、行号感排版与 data-code 一键复制按钮；
 *   - 标准 GFM 表格（| col | col |）解析为响应式精致表格；
 *   - 引用块（> ...）、无序/有序列表、水平分割线；
 *   - 行内代码（`code`）、加粗（**bold**）、链接（区分外链与本项目内部文档互跳 data-internal-doc）。
 */

export interface TocHeading {
    id: string
    level: number
    text: string
}

export interface RenderedMarkdown {
    html: string
    toc: TocHeading[]
    lineCount: number
    charCount: number
    readMinutes: number
}

const escapeHtml = (raw: string): string =>
    raw.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')

export const slugifyHeading = (text: string, idx: number): string => {
    const cleaned = text
        .replace(/`[^`]*`/g, '')
        .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
        .trim()
        .toLowerCase()
        .replace(/[^\w\u4e00-\u9fa5-]+/g, '-')
        .replace(/^-+|-+$/g, '')
    return `doc-sec-${idx}-${cleaned || idx}`
}

const renderInline = (raw: string): string => {
    // 先把行内代码 `...` 抽离为占位符，避免内部字符被二次转义或当成链接/加粗
    const codeSpans: string[] = []
    let work = raw.replace(/`([^`\n]+)`/g, (_m, code: string) => {
        const idx = codeSpans.length
        codeSpans.push(`<code class="md-inline-code">${escapeHtml(code)}</code>`)
        return `\u0000CODE_${idx}\u0000`
    })

    work = escapeHtml(work)

    // Markdown 链接 [label](href)
    work = work.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_m, label: string, href: string) => {
        const cleanHref = href.trim()
        if (/^https?:\/\//i.test(cleanHref) || cleanHref.startsWith('mailto:')) {
            return `<a class="md-link md-link-ext" href="${cleanHref}" target="_blank" rel="noopener noreferrer">${label}<span class="md-ext-arrow">↗</span></a>`
        }
        // 内部文档跳转或页内锚点
        const normalized = cleanHref.replace(/^\.\.\//, '').replace(/^\.\//, '')
        return `<a class="md-link md-link-int" href="javascript:void(0)" data-internal-href="${normalized}">${label}</a>`
    })

    // 加粗 **...**
    work = work.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')

    // 还原行内代码
    work = work.replace(/\u0000CODE_(\d+)\u0000/g, (_m, idxStr: string) => codeSpans[Number(idxStr)] || '')

    return work
}

export function renderMarkdownDocument(source: string, isPlainLicense = false): RenderedMarkdown {
    const normalized = (source || '').replace(/\r\n/g, '\n')
    const lines = normalized.split('\n')
    const lineCount = lines.length
    const charCount = normalized.length
    const readMinutes = Math.max(1, Math.round(charCount / 900))

    if (isPlainLicense) {
        // 针对 LICENSE / NOTICE 等纯文本协议，识别 PART / 条款标题生成 TOC 与分节高可读性排版
        const toc: TocHeading[] = []
        const out: string[] = ['<div class="md-license-sheet">']
        let headingIdx = 0
        for (let i = 0; i < lines.length; i++) {
            const line = lines[i]
            const trimmed = line.trim()
            if (/^={10,}$/.test(trimmed)) {
                continue
            }
            if (/^(PART\s+[I|V|X]+|第[一二三四五六七八九十]+部分|\d+\.\s+[A-Z\u4e00-\u9fa5])/.test(trimmed)) {
                headingIdx++
                const isMajor = /^(PART|第[一二三四五六七八九十]+部分)/.test(trimmed)
                const level = isMajor ? 2 : 3
                const id = slugifyHeading(trimmed, headingIdx)
                toc.push({ id, level, text: trimmed })
                out.push(
                    `<h${level} id="${id}" class="md-heading md-h${level}"><span class="md-anchor-mark">#</span>${escapeHtml(trimmed)}</h${level}>`
                )
            } else if (trimmed === '') {
                out.push('<div class="md-license-gap"></div>')
            } else {
                out.push(`<p class="md-license-line">${escapeHtml(line)}</p>`)
            }
        }
        out.push('</div>')
        return { html: out.join('\n'), toc, lineCount, charCount, readMinutes }
    }

    const toc: TocHeading[] = []
    const htmlBlocks: string[] = []
    let headingCounter = 0
    let codeBlockCounter = 0

    let i = 0
    while (i < lines.length) {
        const line = lines[i]
        const trimmed = line.trim()

        // 1. 围栏代码块 ```
        if (trimmed.startsWith('```')) {
            const lang = trimmed.slice(3).trim() || 'text'
            const codeLines: string[] = []
            i++
            while (i < lines.length && !lines[i].trim().startsWith('```')) {
                codeLines.push(lines[i])
                i++
            }
            i++ // skip closing ```
            codeBlockCounter++
            const rawCode = codeLines.join('\n')
            const encodedCode = encodeURIComponent(rawCode)
            htmlBlocks.push(
                `<div class="md-code-block" data-code-idx="${codeBlockCounter}">` +
                    `<div class="md-code-bar">` +
                    `<span class="md-code-dots"><i></i><i></i><i></i></span>` +
                    `<span class="md-code-lang">${escapeHtml(lang.toUpperCase())}</span>` +
                    `<button type="button" class="md-code-copy-btn" data-copy-code="${encodedCode}">复制代码 / Copy</button>` +
                    `</div>` +
                    `<pre class="md-code-pre"><code>${escapeHtml(rawCode)}</code></pre>` +
                    `</div>`
            )
            continue
        }

        // 2. 水平分割线 ---
        if (/^---+$/.test(trimmed)) {
            htmlBlocks.push('<hr class="md-hr" />')
            i++
            continue
        }

        // 3. 标题 #..####
        const headingMatch = line.match(/^(#{1,4})\s+(.+)$/)
        if (headingMatch) {
            const level = headingMatch[1].length
            const rawTitle = headingMatch[2].trim()
            const plainTitle = rawTitle.replace(/`([^`]+)`/g, '$1').replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
            headingCounter++
            const id = slugifyHeading(plainTitle, headingCounter)
            if (level >= 1 && level <= 3) {
                toc.push({ id, level, text: plainTitle })
            }
            htmlBlocks.push(
                `<h${level} id="${id}" class="md-heading md-h${level}">` +
                    `<a class="md-anchor-mark" href="javascript:void(0)" data-scroll-to="${id}">#</a>` +
                    `<span>${renderInline(rawTitle)}</span>` +
                    `</h${level}>`
            )
            i++
            continue
        }

        // 4. GFM 表格 | ... |
        if (trimmed.startsWith('|') && i + 1 < lines.length && /^\s*\|[\s:|-]+\|\s*$/.test(lines[i + 1])) {
            const parseRow = (rowLine: string) =>
                rowLine
                    .trim()
                    .replace(/^\|/, '')
                    .replace(/\|$/, '')
                    .split('|')
                    .map((c) => renderInline(c.trim()))

            const headers = parseRow(lines[i])
            i += 2 // skip header and separator
            const bodyRows: string[][] = []
            while (i < lines.length && lines[i].trim().startsWith('|')) {
                bodyRows.push(parseRow(lines[i]))
                i++
            }
            const thead = `<thead><tr>${headers.map((h) => `<th>${h}</th>`).join('')}</tr></thead>`
            const tbody = `<tbody>${bodyRows.map((r) => `<tr>${r.map((c) => `<td>${c}</td>`).join('')}</tr>`).join('')}</tbody>`
            htmlBlocks.push(`<div class="md-table-wrap"><table class="md-table">${thead}${tbody}</table></div>`)
            continue
        }

        // 5. 引用块 >
        if (trimmed.startsWith('>')) {
            const quoteLines: string[] = []
            while (i < lines.length && lines[i].trim().startsWith('>')) {
                quoteLines.push(lines[i].trim().replace(/^>\s?/, ''))
                i++
            }
            htmlBlocks.push(`<blockquote class="md-blockquote">${quoteLines.map((q) => `<p>${renderInline(q)}</p>`).join('')}</blockquote>`)
            continue
        }

        // 6. 列表（无序 - / * 或有序 1.）
        if (/^\s*([-*]|\d+\.)\s+/.test(line)) {
            const isOrdered = /^\s*\d+\.\s+/.test(line)
            const tag = isOrdered ? 'ol' : 'ul'
            const items: string[] = []
            while (i < lines.length && (/^\s*([-*]|\d+\.)\s+/.test(lines[i]) || (lines[i].startsWith('  ') && lines[i].trim() !== ''))) {
                const cur = lines[i]
                if (/^\s*([-*]|\d+\.)\s+/.test(cur)) {
                    items.push(renderInline(cur.replace(/^\s*([-*]|\d+\.)\s+/, '')))
                } else if (items.length > 0) {
                    items[items.length - 1] += ' ' + renderInline(cur.trim())
                }
                i++
            }
            htmlBlocks.push(`<${tag} class="md-list">${items.map((it) => `<li>${it}</li>`).join('')}</${tag}>`)
            continue
        }

        // 7. 空行
        if (trimmed === '') {
            i++
            continue
        }

        // 8. 普通段落
        const paraLines: string[] = []
        while (
            i < lines.length &&
            lines[i].trim() !== '' &&
            !lines[i].trim().startsWith('```') &&
            !lines[i].trim().startsWith('#') &&
            !lines[i].trim().startsWith('>') &&
            !lines[i].trim().startsWith('|') &&
            !/^---+$/.test(lines[i].trim()) &&
            !/^\s*([-*]|\d+\.)\s+/.test(lines[i])
        ) {
            paraLines.push(lines[i].trim())
            i++
        }
        if (paraLines.length > 0) {
            htmlBlocks.push(`<p class="md-p">${renderInline(paraLines.join(' '))}</p>`)
        } else {
            i++
        }
    }

    return {
        html: htmlBlocks.join('\n'),
        toc,
        lineCount,
        charCount,
        readMinutes,
    }
}
