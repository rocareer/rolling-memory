<template>
    <div class="rame-home" :class="{ 'is-dark': isDark }" :lang="isZh ? 'zh-CN' : 'en'">
        <a class="skip-link" href="#main-content">{{ t('index.skipToContent') }}</a>
        <div class="ambient-field" aria-hidden="true">
            <span class="ambient-grid" />
        </div>

        <header class="site-header" :class="{ 'is-scrolled': isScrolled }">
            <div class="header-inner">
                <button type="button" class="brand-lockup" :aria-label="t('index.backToTop')" @click="scrollToTop">
                    <span class="brand-mark"><img :src="isDark ? logoYin : logoYang" alt="" /></span>
                    <span class="brand-copy">
                        <strong>Rolling<span>·</span>Memory</strong>
                        <small>{{ t('index.brandTriad') }}</small>
                    </span>
                </button>

                <nav class="desktop-nav" :aria-label="t('index.navAria')">
                    <button
                        v-for="item in navLinks"
                        :key="item.id"
                        type="button"
                        :class="{ 'is-active': activeSection === item.id }"
                        @click="scrollToSection(item.id)"
                    >
                        {{ t(`index.${item.labelKey}`) }}
                    </button>
                </nav>

                <div class="header-actions">
                    <a class="repo-link" :href="repoUrl" target="_blank" rel="noopener noreferrer" :aria-label="t('index.repoAria')">
                        <svg viewBox="0 0 24 24" aria-hidden="true">
                            <path
                                fill="currentColor"
                                d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12"
                            />
                        </svg>
                        <span>GitHub</span>
                    </a>
                    <button type="button" class="icon-button" :title="isDark ? t('login.themeLight') : t('login.themeDark')" @click="toggleDark()">
                        <Icon :name="isDark ? 'lucide-sun' : 'lucide-moon'" :size="17" />
                        <span class="sr-only">{{ isDark ? t('login.themeLight') : t('login.themeDark') }}</span>
                    </button>
                    <button type="button" class="language-button" :aria-label="t('index.languageSwitchAria')" @click="setLang(isZh ? 'en' : 'zh-cn')">
                        <Icon name="lucide-languages" :size="16" /><span>{{ isZh ? 'EN' : '中文' }}</span>
                    </button>
                    <button type="button" class="header-console" @click="goConsole">
                        <span>{{ t('index.enterConsole') }}</span
                        ><Icon name="lucide-arrow-up-right" :size="15" />
                    </button>
                    <button
                        type="button"
                        class="icon-button mobile-toggle"
                        :aria-expanded="mobileOpen"
                        aria-controls="mobile-navigation"
                        :aria-label="mobileOpen ? t('index.closeMenu') : t('index.openMenu')"
                        @click="mobileOpen = !mobileOpen"
                    >
                        <Icon :name="mobileOpen ? 'lucide-x' : 'lucide-menu'" :size="19" />
                    </button>
                </div>
            </div>
            <nav v-if="mobileOpen" id="mobile-navigation" class="mobile-navigation" :aria-label="t('index.navAria')">
                <button v-for="item in navLinks" :key="item.id" type="button" @click="scrollToSection(item.id)">
                    {{ t(`index.${item.labelKey}`) }}<Icon name="lucide-arrow-down-right" :size="16" />
                </button>
                <button type="button" @click="goDocs">{{ t('index.navDocs') }}<Icon name="lucide-arrow-up-right" :size="16" /></button>
            </nav>
        </header>

        <main id="main-content" class="page-content">
            <MemoryField @go-console="goConsole" @scroll-explore="scrollToSection('what')" />

            <!-- 第一屏以下按用户真正关心的问题组织：左侧是问题，右侧是回答；每条承诺旁边写清它的边界 -->
            <section id="what" class="qa" aria-labelledby="what-q">
                <div class="qa-q">
                    <div class="qa-q-inner">
                        <h2 id="what-q">{{ t('index.whatQ') }}</h2>
                    </div>
                </div>
                <div class="qa-a">
                    <p class="qa-lead">{{ t('index.whatLead') }}</p>
                    <p>{{ t('index.whatBody') }}</p>
                    <dl class="contrast">
                        <div class="contrast-row">
                            <dt>{{ t('index.whatWithoutLabel') }}</dt>
                            <dd>{{ t('index.whatWithoutText') }}</dd>
                        </div>
                        <div class="contrast-row is-hit">
                            <dt>{{ t('index.whatWithLabel') }}</dt>
                            <dd>{{ t('index.whatWithText') }}</dd>
                        </div>
                    </dl>
                    <p>{{ t('index.whatCost') }}</p>
                    <p class="qa-note">{{ t('index.whatAuto') }}</p>
                </div>
            </section>

            <section id="cost-privacy" class="qa" aria-labelledby="cost-q">
                <div class="qa-q">
                    <div class="qa-q-inner">
                        <h2 id="cost-q">{{ t('index.costQ') }}</h2>
                    </div>
                </div>
                <div class="qa-a">
                    <p class="qa-lead">{{ t('index.costLead') }}</p>
                    <dl class="promise-list">
                        <template v-for="key in promiseKeys" :key="key">
                            <dt>{{ t(`index.${key}Title`) }}</dt>
                            <dd>{{ t(`index.${key}Text`) }}</dd>
                        </template>
                    </dl>
                    <p class="qa-note">{{ t('index.costNote') }}</p>
                </div>
            </section>

            <section id="savings" class="qa" aria-labelledby="savings-q">
                <div class="qa-q">
                    <div class="qa-q-inner">
                        <h2 id="savings-q">{{ t('index.savingsQ') }}</h2>
                    </div>
                </div>
                <div class="qa-a">
                    <p class="qa-lead">{{ t('index.savingsLead') }}</p>
                    <figure class="ledger">
                        <figcaption>{{ t('index.ledgerCaption', { total: ledger.total }) }}</figcaption>
                        <div class="ledger-bar">
                            <div class="ledger-seg is-helped" :style="{ flexGrow: ledger.helped }">
                                <strong>{{ ledger.helped }}</strong>
                                <span>{{ t('index.ledgerHelpedLabel') }}</span>
                                <small>{{ ledgerShare(ledger.helped) }}</small>
                            </div>
                            <div class="ledger-seg is-idle" :style="{ flexGrow: ledgerIdle }">
                                <strong>{{ ledgerIdle }}</strong>
                                <span>{{ t('index.ledgerIdleLabel') }}</span>
                                <small>{{ ledgerShare(ledgerIdle) }}</small>
                            </div>
                        </div>
                    </figure>
                    <p>{{ t('index.ledgerSaved', { low: ledger.savedLow, high: ledger.savedHigh }) }}</p>
                    <p>{{ t('index.ledgerMiss') }}</p>
                    <p class="qa-note">{{ t('index.ledgerBench', { score: ledger.recallAt10 }) }}</p>
                    <div class="link-row">
                        <button type="button" class="text-link" @click="goDoc('docs-token-economics')">{{ t('index.ledgerDocLink') }}</button>
                        <button type="button" class="text-link" @click="goDoc('docs-benchmarks')">{{ t('index.ledgerBenchLink') }}</button>
                    </div>
                </div>
            </section>

            <section id="start" class="qa" aria-labelledby="start-q">
                <div class="qa-q">
                    <div class="qa-q-inner">
                        <h2 id="start-q">{{ t('index.startQ') }}</h2>
                    </div>
                </div>
                <div class="qa-a">
                    <p class="qa-lead">{{ t('index.startLead') }}</p>
                    <ol class="steps">
                        <li v-for="(step, index) in steps" :key="step.key" class="step">
                            <span class="step-index" aria-hidden="true">{{ index + 1 }}</span>
                            <div class="step-body">
                                <h3>{{ t(`index.${step.key}Title`) }}</h3>
                                <p>{{ t(`index.${step.key}Text`) }}</p>
                                <div v-for="cmd in step.commands" :key="cmd.id" class="cmd-group">
                                    <p v-if="cmd.labelKey" class="cmd-label">{{ t(`index.${cmd.labelKey}`) }}</p>
                                    <div class="cmd">
                                        <pre tabindex="0"><code>{{ cmd.text }}</code></pre>
                                        <button
                                            type="button"
                                            class="cmd-copy"
                                            :class="{ 'is-copied': copiedKey === cmd.id }"
                                            :aria-label="t('index.copyCmd')"
                                            @click="copyText(cmd.text, cmd.id)"
                                        >
                                            <Icon :name="copiedKey === cmd.id ? 'lucide-check' : 'lucide-copy'" :size="14" />
                                            <span>{{ copiedKey === cmd.id ? t('index.copied') : t('index.copy') }}</span>
                                        </button>
                                    </div>
                                </div>
                            </div>
                        </li>
                    </ol>

                    <div class="agents">
                        <h3>{{ t('index.agentsTitle') }}</h3>
                        <p>{{ t('index.agentsAutoText') }}</p>
                        <ul class="agent-list">
                            <li v-for="agent in agentsAuto" :key="agent.id">
                                <span>{{ agent.name }}</span
                                ><code v-if="agent.id !== agent.name">{{ agent.id }}</code>
                            </li>
                        </ul>
                        <p>{{ t('index.agentsNamedText') }}</p>
                        <ul class="agent-list">
                            <li v-for="agent in agentsNamed" :key="agent.id">
                                <span>{{ agent.name }}</span
                                ><code>{{ agent.id }}</code>
                            </li>
                        </ul>
                        <div class="cmd">
                            <pre tabindex="0"><code>{{ namedAgentsCommand }}</code></pre>
                            <button
                                type="button"
                                class="cmd-copy"
                                :class="{ 'is-copied': copiedKey === 'named-agents' }"
                                :aria-label="t('index.copyCmd')"
                                @click="copyText(namedAgentsCommand, 'named-agents')"
                            >
                                <Icon :name="copiedKey === 'named-agents' ? 'lucide-check' : 'lucide-copy'" :size="14" />
                                <span>{{ copiedKey === 'named-agents' ? t('index.copied') : t('index.copy') }}</span>
                            </button>
                        </div>
                        <p class="agents-other">{{ t('index.agentsOther') }}</p>
                    </div>

                    <div class="console-cta">
                        <p>{{ t('index.consoleText') }}</p>
                        <button type="button" class="button-primary" @click="goConsole">{{ t('index.enterConsole') }}</button>
                    </div>
                </div>
            </section>

            <section id="pulse-preview-section" class="qa" aria-labelledby="preview-q">
                <div class="qa-q">
                    <div class="qa-q-inner">
                        <h2 id="preview-q">{{ t('index.previewQ') }}</h2>
                        <span class="status-tag">{{ t('index.previewTag') }}</span>
                    </div>
                </div>
                <div class="qa-a">
                    <p class="qa-lead">{{ t('index.previewLead') }}</p>
                    <p>{{ t('index.previewBody') }}</p>
                    <dl class="contact-list">
                        <div>
                            <dt>{{ t('index.previewApply') }}</dt>
                            <dd>
                                <a :href="`mailto:${contacts.apply}`">{{ contacts.apply }}</a>
                            </dd>
                        </div>
                        <div>
                            <dt>{{ t('index.previewSupport') }}</dt>
                            <dd>
                                <a :href="`mailto:${contacts.support}`">{{ contacts.support }}</a>
                            </dd>
                        </div>
                    </dl>
                </div>
            </section>

            <section id="about" class="qa" aria-labelledby="about-q">
                <div class="qa-q">
                    <div class="qa-q-inner">
                        <h2 id="about-q">{{ t('index.aboutQ') }}</h2>
                    </div>
                </div>
                <div class="qa-a">
                    <p class="qa-lead">{{ t('index.aboutLead') }}</p>
                    <p>{{ t('index.aboutBody') }}</p>
                    <p>{{ t('index.aboutLicense') }}</p>
                    <ul class="link-row">
                        <li>
                            <a class="text-link" :href="repoUrl" target="_blank" rel="noopener noreferrer">{{ t('index.linkGithub') }}</a>
                        </li>
                        <li>
                            <button type="button" class="text-link" @click="goPath('/admin/help/repos')">{{ t('index.linkMirror') }}</button>
                        </li>
                        <li>
                            <button type="button" class="text-link" @click="goDocs">{{ t('index.linkDocs') }}</button>
                        </li>
                        <li>
                            <button type="button" class="text-link" @click="goDoc('root-declaration')">{{ t('index.linkDeclaration') }}</button>
                        </li>
                        <li>
                            <a class="text-link" :href="`mailto:${contacts.support}`">{{ t('index.contactTeam') }}</a>
                        </li>
                    </ul>
                </div>
            </section>
        </main>

        <footer class="site-footer">
            <div class="footer-brand">
                <img :src="isDark ? logoYin : logoYang" alt="Rolling Memory" /><span>Rolling·Memory</span
                ><small>{{ t('index.footerTagline') }}</small>
            </div>
            <div class="footer-meta">
                <span>{{ t('index.footerNote') }}</span
                ><span>© {{ new Date().getFullYear() }} Rocareer &amp; Rolling-Memory Organization</span><span>{{ releaseVersion }}</span>
            </div>
        </footer>
    </div>
</template>

<script setup lang="ts">
import logoYang from '@/assets/logo-yang.svg'
import logoYin from '@/assets/logo-yin.svg'
import { useDark } from '@/hooks/useDark'
import { setLang } from '@/lang'
import { useConfig } from '@/stores/config'
import MemoryField from '@/views/home/MemoryField.vue'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

interface CommandBlock {
    id: string
    text: string
    labelKey?: string
}

const { t } = useI18n()
const router = useRouter()
const config = useConfig()
const { isDark, toggleDark } = useDark()
const isZh = computed(() => config.lang.active === 'zh-cn')
const releaseVersion = 'v' + __RM_VERSION__
const repoUrl = 'https://github.com/rocareer/rolling-memory'
const isScrolled = ref(false)
const mobileOpen = ref(false)
const activeSection = ref('what')
const copiedKey = ref('')
let copyTimer: ReturnType<typeof setTimeout> | null = null

const navLinks = [
    { id: 'what', labelKey: 'navWhat' },
    { id: 'cost-privacy', labelKey: 'navCost' },
    { id: 'savings', labelKey: 'navSavings' },
    { id: 'start', labelKey: 'navStart' },
    { id: 'pulse-preview-section', labelKey: 'navPreview' },
]
const promiseKeys = ['promiseFree', 'promiseLive', 'promiseLocal', 'promiseOwn']

// 读数与公开 README 一致：作者开发机 24 小时逐轮自评；Recall@10 为 ATM-Bench 官方口径（逐题金标覆盖率均值）
const ledger = { total: 76, helped: 36, savedLow: 46, savedHigh: 67, recallAt10: '76.72%' }
const ledgerIdle = ledger.total - ledger.helped
const ledgerShare = (count: number) => `${((count / ledger.total) * 100).toFixed(1)}%`

// 命令须与 CLI 实际参数一致：install 的目标 Agent 是位置参数，不存在 --targets
const steps: { key: string; commands: CommandBlock[] }[] = [
    { key: 'stepInstall', commands: [{ id: 'install', text: `git clone ${repoUrl}.git\ncd rolling-memory && bash install.sh` }] },
    { key: 'stepCheck', commands: [{ id: 'check', text: 'rolling-memory install --check' }] },
    {
        key: 'stepAdjust',
        commands: [
            { id: 'mcp-only', labelKey: 'adjustMcpOnly', text: 'rolling-memory install --uninstall\nrolling-memory install --mcp-only' },
            {
                id: 'hooks-off',
                labelKey: 'adjustHooksOff',
                text: 'rolling-memory config set hooks.ledger false',
            },
            { id: 'uninstall', labelKey: 'adjustUninstall', text: 'rolling-memory install --uninstall' },
        ],
    },
]
// 不带参数的 install 只接入检测到的这五个；Cursor / Windsurf / Cline 需显式点名
const agentsAuto = [
    { name: 'ZCode', id: 'zcode' },
    { name: 'Claude Code', id: 'claude-code' },
    { name: 'Codex', id: 'codex' },
    { name: 'CodeBuddy', id: 'codebuddy' },
    { name: 'dsh', id: 'dsh' },
]
const agentsNamed = [
    { name: 'Cursor', id: 'cursor' },
    { name: 'Windsurf', id: 'windsurf' },
    { name: 'Cline', id: 'cline' },
]
const namedAgentsCommand = 'rolling-memory install cursor windsurf cline'
const contacts = { apply: 'pick@psychologylake.org.cn', support: 'support@psychologylake.org.cn' }

function goConsole() {
    goPath('/admin/dashboard')
}
function goDocs() {
    goPath('/admin/help/docs')
}
function goDoc(docId: string) {
    mobileOpen.value = false
    router.push({ path: '/admin/help/docs', query: { doc: docId } })
}
function goPath(path: string) {
    mobileOpen.value = false
    router.push(path)
}
function scrollToTop() {
    window.scrollTo({ top: 0, behavior: 'smooth' })
}
function scrollToSection(id: string) {
    mobileOpen.value = false
    activeSection.value = id
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
async function copyText(text: string, key: string) {
    try {
        await navigator.clipboard.writeText(text)
    } catch {
        const textarea = document.createElement('textarea')
        textarea.value = text
        textarea.style.position = 'fixed'
        textarea.style.opacity = '0'
        document.body.appendChild(textarea)
        textarea.select()
        document.execCommand('copy')
        textarea.remove()
    }
    copiedKey.value = key
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => {
        if (copiedKey.value === key) copiedKey.value = ''
    }, 1800)
}
function handleScroll() {
    isScrolled.value = window.scrollY > 16
    for (const item of navLinks) {
        const section = document.getElementById(item.id)
        if (!section) continue
        const rect = section.getBoundingClientRect()
        if (rect.top <= 180 && rect.bottom > 180) {
            activeSection.value = item.id
            break
        }
    }
}

onMounted(() => {
    window.addEventListener('scroll', handleScroll, { passive: true })
})
onBeforeUnmount(() => {
    window.removeEventListener('scroll', handleScroll)
    if (copyTimer) clearTimeout(copyTimer)
})
</script>

<style scoped lang="scss">
.rame-home {
    --bg: #f6f7f9;
    --bg-deep: #efeff3;
    --surface: rgba(255, 255, 255, 0.74);
    --surface-solid: #ffffff;
    --ink: #0f1014;
    --text: #272830;
    --muted: rgba(39, 40, 48, 0.66);
    --line: rgba(15, 16, 20, 0.11);
    --line-strong: rgba(15, 16, 20, 0.24);
    --cyan: #0284c7;
    --link: #0369a1;
    --hit: #0369a1;
    --hit-ink: #ffffff;
    --preview: #db2777;
    --preview-ink: #be185d;
    --glow: rgba(15, 16, 20, 0.035);
    --mono: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
    position: relative;
    min-height: 100dvh;
    /* clip 而非 hidden：hidden 会把本容器变成滚动容器，导致顶栏与问题栏的 sticky 失效 */
    overflow-x: clip;
    color: var(--text);
    background: var(--bg);
    font-family: var(--rm-brand-font), Inter, 'PingFang SC', 'Microsoft YaHei', sans-serif;
    font-feature-settings: 'cv02', 'cv03', 'cv04', 'cv11';
    transition:
        background-color 0.25s ease,
        color 0.25s ease;
}
.rame-home.is-dark,
html.dark .rame-home {
    --bg: #090a0d;
    --bg-deep: #0e1015;
    --surface: rgba(20, 22, 29, 0.74);
    --surface-solid: #14161d;
    --ink: #f5f5f7;
    --text: #e6e7ee;
    --muted: rgba(230, 231, 238, 0.62);
    --line: rgba(255, 255, 255, 0.1);
    --line-strong: rgba(255, 255, 255, 0.24);
    --cyan: #38bdf8;
    --link: #7dd3fc;
    --hit: #38bdf8;
    --hit-ink: #031019;
    --preview: #ec4899;
    --preview-ink: #f9a8d4;
    --glow: rgba(255, 255, 255, 0.025);
}
button,
a {
    -webkit-tap-highlight-color: transparent;
    touch-action: manipulation;
}
button {
    font: inherit;
}
button:focus-visible,
a:focus-visible,
[tabindex]:focus-visible {
    outline: 2px solid var(--cyan);
    outline-offset: 5px;
}
button:disabled {
    cursor: not-allowed;
}
.sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
}
.skip-link {
    position: fixed;
    z-index: 200;
    top: 12px;
    left: 16px;
    padding: 10px 16px;
    color: var(--bg);
    background: var(--cyan);
    border-radius: 4px;
    transform: translateY(-160%);
}
.skip-link:focus {
    transform: translateY(0);
}
.ambient-field {
    position: fixed;
    z-index: 0;
    inset: 0;
    overflow: hidden;
    pointer-events: none;
}
.ambient-grid {
    position: absolute;
    inset: 0;
    opacity: 0.22;
    background-image: linear-gradient(var(--line) 1px, transparent 1px), linear-gradient(90deg, var(--line) 1px, transparent 1px);
    background-size: 96px 96px;
    mask-image: radial-gradient(ellipse 80% 60% at 50% 25%, #000 20%, transparent 90%);
}

/* ---------- 顶栏 ---------- */
.site-header {
    position: sticky;
    z-index: 100;
    top: 0;
    border-bottom: 1px solid transparent;
    background: color-mix(in srgb, var(--bg) 78%, transparent);
    backdrop-filter: blur(20px) saturate(150%);
    transition:
        border-color 0.2s ease,
        box-shadow 0.2s ease;
}
.site-header.is-scrolled {
    border-color: var(--line);
    box-shadow: 0 12px 40px -30px rgba(0, 0, 0, 0.7);
}
.header-inner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 28px;
    max-width: 1440px;
    height: 82px;
    padding: 0 42px;
    margin: auto;
}
.brand-lockup {
    display: inline-flex;
    align-items: center;
    gap: 12px;
    padding: 0;
    color: var(--ink);
    background: none;
    border: 0;
    cursor: pointer;
    text-align: left;
    flex-shrink: 0;
}
.brand-mark {
    display: grid;
    place-items: center;
    width: 36px;
    height: 36px;
    border: 1px solid var(--line-strong);
    border-radius: 50%;
    box-shadow: inset 0 0 16px var(--glow);
}
.brand-mark img {
    width: 22px;
    height: 22px;
}
.brand-copy {
    display: flex;
    flex-direction: column;
    gap: 4px;
}
.brand-copy strong {
    font-size: 16px;
    font-weight: 750;
    letter-spacing: -0.04em;
}
.brand-copy strong span {
    margin: 0 3px;
    color: var(--cyan);
}
.brand-copy small {
    color: var(--muted);
    font-family: var(--mono);
    font-size: 8px;
    letter-spacing: 0.1em;
}
.desktop-nav {
    display: flex;
    align-items: center;
    gap: 4px;
}
.desktop-nav button {
    position: relative;
    padding: 10px 12px;
    color: var(--muted);
    font-size: 13px;
    background: none;
    border: 0;
    cursor: pointer;
    white-space: nowrap;
    transition: color 0.18s ease;
}
.desktop-nav button::after {
    position: absolute;
    right: 12px;
    bottom: 3px;
    left: 12px;
    height: 1px;
    content: '';
    background: var(--cyan);
    transform: scaleX(0);
    transform-origin: left;
    transition: transform 0.2s ease;
}
.desktop-nav button:hover,
.desktop-nav button.is-active {
    color: var(--ink);
}
.desktop-nav button.is-active::after {
    transform: scaleX(1);
}
.header-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
}
.repo-link {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 8px 9px;
    color: var(--muted);
    font-size: 12px;
    text-decoration: none;
}
.repo-link:hover {
    color: var(--ink);
}
.repo-link svg {
    width: 16px;
    height: 16px;
}
.icon-button,
.language-button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    height: 34px;
    min-width: 34px;
    padding: 0 8px;
    color: var(--text);
    background: transparent;
    border: 1px solid transparent;
    border-radius: 5px;
    cursor: pointer;
}
.icon-button:hover,
.language-button:hover {
    border-color: var(--line);
    background: var(--surface);
}
.language-button {
    font-family: var(--mono);
    font-size: 11px;
}
.header-console {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    height: 36px;
    padding: 0 14px;
    margin-left: 4px;
    color: var(--bg);
    font-size: 12px;
    font-weight: 650;
    white-space: nowrap;
    background: var(--ink);
    border: 1px solid var(--ink);
    border-radius: 4px;
    cursor: pointer;
    transition:
        background 0.18s ease,
        color 0.18s ease;
}
.header-console:hover {
    color: var(--ink);
    background: transparent;
}
.mobile-toggle,
.mobile-navigation {
    display: none;
}
.page-content {
    position: relative;
    z-index: 1;
    max-width: 1440px;
    padding: 0 42px;
    margin: 0 auto;
}

/* ---------- 问答区：左问右答 ---------- */
.qa {
    display: grid;
    grid-template-columns: minmax(180px, 280px) minmax(0, 1fr);
    gap: clamp(28px, 5vw, 80px);
    padding: clamp(64px, 8vw, 112px) 0;
    border-bottom: 1px solid var(--line);
    scroll-margin-top: 82px;
}
.qa:last-of-type {
    border-bottom: 0;
}
.qa-q-inner {
    position: sticky;
    top: 112px;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 12px;
}
.qa-q h2 {
    margin: 0;
    color: var(--ink);
    font-size: clamp(18px, 1.5vw, 21px);
    font-weight: 600;
    line-height: 1.45;
    letter-spacing: -0.01em;
    text-wrap: balance;
}
.qa-a {
    min-width: 0;
    max-width: 760px;
}
.qa-a > p {
    max-width: 42em;
    margin: 0 0 16px;
    color: var(--text);
    font-size: 15.5px;
    line-height: 1.85;
    text-wrap: pretty;
}
.qa-a > .qa-lead {
    max-width: 18em;
    margin-bottom: 24px;
    color: var(--ink);
    font-size: clamp(26px, 3.1vw, 40px);
    font-weight: 600;
    letter-spacing: -0.035em;
    line-height: 1.2;
    text-wrap: balance;
}
.qa-a > .qa-note {
    margin-top: 24px;
    color: var(--muted);
    font-size: 14px;
    line-height: 1.8;
}
.text-link {
    display: inline-flex;
    padding: 0 0 3px;
    color: var(--link);
    font-size: 14.5px;
    text-decoration: none;
    white-space: nowrap;
    background: none;
    border: 0;
    border-bottom: 1px solid color-mix(in srgb, var(--link) 40%, transparent);
    cursor: pointer;
}
/* 标题与短标签按短语断行：中文只在标点和空格处换行，避免「看不到」被拆成「看不 / 到」；正文仍逐字换行 */
.qa-q h2,
.qa-a > .qa-lead,
.contrast dt,
.step-body h3,
.agents h3,
.cmd-label,
.contact-list dt {
    word-break: keep-all;
    overflow-wrap: break-word;
}
.text-link:hover {
    border-color: var(--link);
}
.link-row {
    display: flex;
    flex-wrap: wrap;
    gap: 12px 28px;
    padding: 0;
    margin: 24px 0 0;
    list-style: none;
}

/* 有无记忆的对比 */
.contrast {
    margin: 28px 0 24px;
    overflow: hidden;
    border: 1px solid var(--line);
    border-radius: 10px;
}
.contrast-row {
    display: grid;
    grid-template-columns: 9.5em minmax(0, 1fr);
    gap: 6px 24px;
    align-items: baseline;
    padding: 16px 20px;
}
.contrast-row + .contrast-row {
    border-top: 1px solid var(--line);
}
.contrast dt {
    color: var(--muted);
    font-size: 13.5px;
    font-weight: 600;
    white-space: nowrap;
}
.contrast dd {
    margin: 0;
    color: var(--text);
    font-size: 15px;
    line-height: 1.7;
    text-wrap: pretty;
}
.contrast-row.is-hit {
    background: color-mix(in srgb, var(--cyan) 8%, transparent);
}
.contrast-row.is-hit dt {
    color: var(--link);
}

/* 费用与隐私：标题列按最长标题自适应，标题永不折行 */
.promise-list {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    margin: 8px 0 0;
}
.promise-list dt,
.promise-list dd {
    padding: 20px 0;
    border-top: 1px solid var(--line);
}
.promise-list dt {
    padding-right: 32px;
    color: var(--ink);
    font-size: 16px;
    font-weight: 650;
    white-space: nowrap;
}
.promise-list dd {
    margin: 0;
    color: var(--text);
    font-size: 15px;
    line-height: 1.8;
    text-wrap: pretty;
}

/* 账本：全页唯一的强调元素 */
.ledger {
    margin: 8px 0 28px;
}
.ledger figcaption {
    margin-bottom: 14px;
    color: var(--muted);
    font-size: 14px;
    line-height: 1.7;
    text-wrap: pretty;
}
.ledger-bar {
    display: flex;
    gap: 6px;
    min-height: 128px;
}
.ledger-seg {
    display: flex;
    flex: 1 1 0;
    flex-direction: column;
    justify-content: flex-end;
    gap: 4px;
    min-width: 0;
    padding: 16px 18px;
    border-radius: 10px;
}
.ledger-seg strong {
    font-size: clamp(36px, 4vw, 52px);
    font-weight: 650;
    font-variant-numeric: tabular-nums;
    letter-spacing: -0.04em;
    line-height: 1;
}
.ledger-seg span {
    font-size: 14px;
    font-weight: 600;
    white-space: nowrap;
}
.ledger-seg small {
    font-size: 13px;
    font-variant-numeric: tabular-nums;
    opacity: 0.82;
}
.ledger-seg.is-helped {
    color: var(--hit-ink);
    background: var(--hit);
}
.ledger-seg.is-idle {
    color: var(--text);
    border: 1px solid var(--line-strong);
}

/* 开始使用：真实顺序，所以用序号 */
.steps {
    padding: 0;
    margin: 8px 0 0;
    list-style: none;
}
.step {
    display: grid;
    grid-template-columns: 2.25rem minmax(0, 1fr);
    gap: 16px;
    padding: 24px 0;
    border-top: 1px solid var(--line);
}
.step-index {
    display: grid;
    place-items: center;
    width: 2.25rem;
    height: 2.25rem;
    color: var(--ink);
    font-size: 14px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    border: 1px solid var(--line-strong);
    border-radius: 50%;
}
.step-body {
    min-width: 0;
}
.step-body h3,
.agents h3 {
    margin: 4px 0 8px;
    color: var(--ink);
    font-size: 17px;
    font-weight: 650;
}
.step-body > p,
.agents > p {
    max-width: 40em;
    margin: 0 0 14px;
    color: var(--text);
    font-size: 15px;
    line-height: 1.8;
    text-wrap: pretty;
}
.cmd-group + .cmd-group {
    margin-top: 16px;
}
.cmd-label {
    margin: 0 0 8px;
    color: var(--muted);
    font-size: 13.5px;
    line-height: 1.6;
    text-wrap: pretty;
}
.cmd {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 10px 10px 10px 16px;
    background: var(--bg-deep);
    border: 1px solid var(--line);
    border-radius: 8px;
}
/* 命令不断词：放不下就横向滚动，避免一条命令被拆成看似两条 */
.cmd pre {
    flex: 1;
    min-width: 0;
    padding: 5px 0;
    margin: 0;
    overflow-x: auto;
    color: var(--ink);
    font-family: var(--mono);
    font-size: 13px;
    line-height: 1.75;
    white-space: pre;
    scrollbar-width: thin;
}
.cmd-copy {
    display: inline-flex;
    flex-shrink: 0;
    align-items: center;
    gap: 6px;
    height: 32px;
    padding: 0 10px;
    color: var(--muted);
    font-size: 12.5px;
    white-space: nowrap;
    background: transparent;
    border: 1px solid var(--line);
    border-radius: 6px;
    cursor: pointer;
}
.cmd-copy:hover {
    color: var(--ink);
    border-color: var(--line-strong);
}
.cmd-copy.is-copied {
    color: var(--link);
    border-color: color-mix(in srgb, var(--link) 45%, transparent);
}
.agents {
    padding-top: 24px;
    margin-top: 8px;
    border-top: 1px solid var(--line);
}
.agent-list {
    display: flex;
    flex-wrap: wrap;
    gap: 10px 24px;
    padding: 0;
    margin: 12px 0 20px;
    list-style: none;
}
.agent-list li {
    display: inline-flex;
    align-items: baseline;
    gap: 8px;
    color: var(--ink);
    font-size: 15px;
    font-weight: 550;
    white-space: nowrap;
}
.agent-list code {
    color: var(--muted);
    font-family: var(--mono);
    font-size: 12px;
    font-weight: 400;
}
.agents > .agents-other {
    margin-top: 14px;
    color: var(--muted);
    font-size: 14px;
}
.console-cta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 16px 24px;
    padding: 20px 22px;
    margin-top: 32px;
    border: 1px solid var(--line);
    border-radius: 10px;
}
.console-cta p {
    flex: 1 1 18em;
    margin: 0;
    color: var(--text);
    font-size: 15px;
    line-height: 1.7;
    text-wrap: pretty;
}
.button-primary {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 44px;
    padding: 0 20px;
    color: var(--bg);
    font-size: 14px;
    font-weight: 600;
    white-space: nowrap;
    background: var(--ink);
    border: 1px solid var(--ink);
    border-radius: 6px;
    cursor: pointer;
    transition:
        color 0.18s ease,
        background 0.18s ease;
}
.button-primary:hover {
    color: var(--ink);
    background: transparent;
}

/* 预览：与第一屏「预览功能」徽标同色 */
.status-tag {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 10px;
    color: var(--preview-ink);
    font-size: 12.5px;
    font-weight: 650;
    white-space: nowrap;
    background: color-mix(in srgb, var(--preview) 12%, transparent);
    border: 1px solid color-mix(in srgb, var(--preview) 45%, transparent);
    border-radius: 999px;
}
.status-tag::before {
    width: 6px;
    height: 6px;
    content: '';
    background: currentColor;
    border-radius: 50%;
}
.contact-list {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 10px 24px;
    padding: 18px 20px;
    margin: 24px 0 0;
    border: 1px solid var(--line);
    border-radius: 10px;
}
.contact-list > div {
    display: contents;
}
.contact-list dt {
    color: var(--muted);
    font-size: 14px;
    line-height: 1.7;
    white-space: nowrap;
}
.contact-list dd {
    min-width: 0;
    margin: 0;
    font-size: 15px;
    line-height: 1.7;
    overflow-wrap: anywhere;
}
.contact-list a {
    color: var(--link);
    text-decoration: none;
    border-bottom: 1px solid color-mix(in srgb, var(--link) 35%, transparent);
}
.contact-list a:hover {
    border-color: var(--link);
}

/* ---------- 页脚 ---------- */
.site-footer {
    position: relative;
    z-index: 1;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 30px;
    max-width: 1440px;
    padding: 32px 42px 40px;
    margin: 0 auto;
    border-top: 1px solid var(--line);
}
.footer-brand {
    display: flex;
    align-items: center;
    gap: 10px;
}
.footer-brand img {
    width: 18px;
    height: 18px;
}
.footer-brand > span {
    color: var(--ink);
    font-size: 13px;
    font-weight: 650;
    white-space: nowrap;
}
.footer-brand small {
    padding-left: 10px;
    color: var(--muted);
    font-size: 12px;
    border-left: 1px solid var(--line);
}
.footer-meta {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 4px;
    color: var(--muted);
    font-size: 12px;
    line-height: 1.6;
}

@media (max-width: 1240px) {
    .header-inner {
        gap: 16px;
        padding: 0 28px;
    }
    .desktop-nav button {
        padding: 10px 8px;
        font-size: 12px;
    }
    .brand-copy small {
        font-size: 7px;
    }
    .repo-link {
        display: none;
    }
    .page-content {
        padding: 0 28px;
    }
    .site-footer {
        padding-right: 28px;
        padding-left: 28px;
    }
}
@media (max-width: 1020px) {
    .desktop-nav {
        display: none;
    }
    .mobile-toggle {
        display: inline-flex;
    }
    .mobile-navigation {
        display: grid;
        grid-template-columns: repeat(3, minmax(0, 1fr));
        gap: 1px;
        padding: 12px 28px 20px;
        background: var(--bg);
        border-top: 1px solid var(--line);
    }
    .mobile-navigation button {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        padding: 14px 12px;
        color: var(--text);
        font-size: 13px;
        text-align: left;
        background: var(--surface);
        border: 0;
        cursor: pointer;
    }
    .footer-brand small {
        display: none;
    }
}
@media (max-width: 860px) {
    .qa {
        grid-template-columns: 1fr;
        gap: 16px;
    }
    .qa-q-inner {
        position: static;
        flex-direction: row;
        flex-wrap: wrap;
        align-items: center;
    }
}
@media (max-width: 680px) {
    .header-inner {
        height: 68px;
        padding: 0 18px;
        gap: 8px;
    }
    .brand-copy strong {
        font-size: 14px;
    }
    .brand-copy small {
        display: none;
    }
    .brand-mark {
        width: 32px;
        height: 32px;
    }
    .brand-mark img {
        width: 19px;
        height: 19px;
    }
    .header-actions {
        gap: 2px;
    }
    .header-console {
        display: none;
    }
    .language-button {
        padding: 0 6px;
        font-size: 10px;
    }
    .mobile-navigation {
        grid-template-columns: repeat(2, minmax(0, 1fr));
        padding: 10px 18px 16px;
    }
    .page-content {
        padding: 0 18px;
    }
    .qa {
        padding: 56px 0;
    }
    .qa-a > p {
        font-size: 15px;
    }
    /* 窄屏：复制按钮压成纯图标，把宽度留给命令本身 */
    .cmd {
        gap: 8px;
        padding-left: 12px;
    }
    .cmd pre {
        font-size: 12.5px;
    }
    .cmd-copy {
        width: 32px;
        padding: 0;
        justify-content: center;
    }
    .cmd-copy span {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip: rect(0, 0, 0, 0);
        white-space: nowrap;
    }
    .site-footer {
        align-items: flex-start;
        flex-direction: column;
        gap: 16px;
        padding: 28px 18px;
    }
    .footer-meta {
        align-items: flex-start;
    }
}
@media (max-width: 560px) {
    .contrast-row {
        grid-template-columns: 1fr;
        gap: 4px;
    }
    .promise-list {
        grid-template-columns: 1fr;
    }
    .promise-list dt {
        padding: 18px 0 6px;
    }
    .promise-list dd {
        padding: 0 0 18px;
        border-top: 0;
    }
    .ledger-bar {
        min-height: 112px;
    }
    .ledger-seg {
        padding: 14px;
    }
    .step {
        grid-template-columns: 2rem minmax(0, 1fr);
        gap: 12px;
    }
    .step-index {
        width: 2rem;
        height: 2rem;
        font-size: 13px;
    }
    .contact-list {
        grid-template-columns: 1fr;
        gap: 2px;
    }
    .contact-list > div + div dt {
        margin-top: 10px;
    }
}
@media (prefers-reduced-motion: reduce) {
    *,
    *::before,
    *::after {
        scroll-behavior: auto !important;
        transition-duration: 0.01ms !important;
        animation-duration: 0.01ms !important;
        animation-iteration-count: 1 !important;
    }
}
</style>
