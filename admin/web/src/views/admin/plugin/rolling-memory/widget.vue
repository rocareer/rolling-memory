<!-- plugin: rolling-memory v0.0.58 -->
<!--
    Rolling-Memory 记忆系统开发调试实时监控浮标（布局级插件组件）。
    源：plugins/rolling-memory/web/views/widget.vue；落位副本：web/src/views/admin/plugin/rolling-memory/widget.vue。
    由宿主布局 web/src/layouts/admin/components/pluginWidgets.vue 自动扫描挂载（与左侧 DEV 环境浮标同行紧邻并排）。
    当插件停用/未装或用户在监控面板关闭 widget_enabled 时自动隐藏（fail-closed）。
-->
<template>
    <el-tooltip
        v-if="info && info.enabled"
        placement="top"
        effect="dark"
        :show-after="220"
        :enterable="false"
        :hide-after="0"
        popper-class="rm-widget-popper"
    >
        <template #content>
            <div class="rm-widget-tip">
                <div class="rm-widget-tip-title">
                    <span>Rolling Memory v{{ info.version }}</span>
                    <span class="mode-indicator">{{ info.mode.toUpperCase() }}</span>
                    <span class="ecg-bpm-tag" :class="{ offline: !info.upstream_online }">
                        ♥ 1Hz {{ isLocalMode ? 'LOCAL VAULT' : 'UPSTREAM' }} ·
                        {{ info.upstream_online ? `${info.upstream_rtt_ms}ms RTT` : 'OFFLINE' }}
                    </span>
                </div>
                <!-- 浮窗内联 264×54 采样滚动心电监护屏 (24 秒环形缓冲区)：本地模式采样本地 SQLite 明文金库，云端模式采样 /open/memory/v1 接口 -->
                <div class="rm-widget-ecg-screen" :class="{ offline: !info.upstream_online }">
                    <svg class="rm-tip-ecg-svg" viewBox="0 0 264 54" preserveAspectRatio="none">
                        <defs>
                            <pattern id="rm-ecg-grid" width="11" height="11" patternUnits="userSpaceOnUse">
                                <path d="M 11 0 L 0 0 0 11" fill="none" stroke="rgba(16, 185, 129, 0.12)" stroke-width="0.6" />
                            </pattern>
                        </defs>
                        <rect x="0" y="0" width="264" height="54" fill="url(#rm-ecg-grid)" />
                        <!-- 等电位静息基线 (y=34) -->
                        <line x1="0" y1="34" x2="264" y2="34" class="ecg-iso-line" />
                        <!-- 24 秒每秒真实采样上游接口生成的 P-Q-R-S-T 连续滚动心电图 -->
                        <path :d="tipRollingEcgPath.path" class="ecg-live-trace" />
                        <!-- 最右侧当前第 0 秒实时采样荧光探头圆点 -->
                        <circle
                            v-if="info.upstream_online"
                            :cx="tipRollingEcgPath.headX"
                            :cy="tipRollingEcgPath.headY"
                            r="2.6"
                            class="ecg-playhead-dot"
                        />
                    </svg>
                    <div class="ecg-lead-label">
                        {{ isLocalMode ? 'LOCAL VAULT' : 'UPSTREAM' }}
                        {{ info.upstream_endpoint || (isLocalMode ? 'local://vault' : '/open/memory/v1') }} · {{ lastTickLabel }}
                    </div>
                </div>
                <div>
                    {{ t('plugin.rolling-memory.widget.status') }}:
                    {{ info.upstream_online ? t('plugin.rolling-memory.widget.online') : t('plugin.rolling-memory.widget.offline') }} · Harness:
                    {{ info.active_harness || '—' }} · {{ t('plugin.rolling-memory.widget.scope') }}: {{ info.scope }}
                </div>
                <div>
                    {{ t('plugin.rolling-memory.widget.assertions') }}: {{ info.active_assertions }} ·
                    {{ isLocalMode ? 'Local Vault' : 'Upstream' }} RTT: {{ info.upstream_rtt_ms }}ms
                </div>
                <div>
                    {{ t('plugin.rolling-memory.widget.lastEvent') }}: {{ info.last_event }} · {{ t('plugin.rolling-memory.widget.latency') }}:
                    {{ info.last_recall_ms }}ms
                </div>
                <div class="rm-widget-tip-meta">
                    {{ t('plugin.rolling-memory.widget.boot') }}: {{ info.boot_context ? 'ON' : 'OFF' }} ·
                    {{ t('plugin.rolling-memory.widget.ledger') }}: {{ info.ledger ? 'ON' : 'OFF' }}
                </div>
                <div v-if="info.bg_task" class="rm-widget-tip-meta">{{ t('plugin.rolling-memory.widget.bgTask') }}: {{ info.bg_task }}</div>
                <div class="rm-widget-tip-action">{{ t('plugin.rolling-memory.widget.clickTerminal') }}</div>
            </div>
        </template>
        <span
            class="rm-widget-badge"
            :class="info.upstream_online ? 'is-online' : 'is-offline'"
            role="button"
            tabindex="0"
            data-testid="rm-widget-badge"
            @click="openTerminal"
            @keydown.enter="openTerminal"
        >
            <span class="rm-widget-dot" :class="{ beating: lastTickFired }"></span>
            <!-- 胶囊内联 60×16 上游接口每秒真实采样滚动心电监视窗 (最近 8 秒) -->
            <span class="rm-mini-ecg-box">
                <svg class="rm-widget-mini-ecg" viewBox="0 0 60 16" preserveAspectRatio="none">
                    <line x1="0" y1="10.5" x2="60" y2="10.5" class="mini-ecg-axis" />
                    <path :d="miniRollingEcgPath.path" class="mini-ecg-trace" />
                    <circle
                        v-if="info.upstream_online"
                        :cx="miniRollingEcgPath.headX"
                        :cy="miniRollingEcgPath.headY"
                        r="1.6"
                        class="ecg-playhead-dot"
                    />
                </svg>
            </span>
            <span class="rm-widget-mode-chip">{{ (info.mode || 'local').toUpperCase() }}</span>
            <span class="rm-widget-label">
                RM {{ info.active_assertions }}<template v-if="info.bg_task"> · {{ info.bg_task }}</template>
            </span>
        </span>
    </el-tooltip>
</template>

<script setup lang="ts">
import { useAdminInfo } from '@/stores/adminInfo'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
// 本地模式（local_plain）：浮标 1Hz 采样本地明文金库往返，零云端请求；由状态面 mode 字段判定。
const isLocalMode = computed(() => String(info.value?.mode || 'local') !== 'cloud')
const terminalModules: Record<string, any> = import.meta.glob('@/stores/terminal.ts', { eager: true })
const useTerminalFn = Object.values(terminalModules)[0]?.useTerminal

const openTerminal = () => {
    if (typeof useTerminalFn === 'function') {
        const terminal = useTerminalFn()
        terminal?.toggle?.(true)
    }
}

interface WidgetInfo {
    enabled: boolean
    online: boolean
    upstream_online: boolean
    upstream_rtt_ms: number
    upstream_endpoint?: string
    upstream_engine?: string
    active_harness?: string
    version: string
    mode: string
    scope: string
    active_assertions: number
    conflicts_proposed: number
    last_event: string
    last_event_ts?: number
    event_count?: number
    last_recall_ms: number
    boot_context: boolean
    ledger: boolean
    bg_task?: string
    sampled_at_ms?: number
}

// 单秒上游接口真实采样点：
// 每一秒直接反映上游 `/open/memory/v1/health` 实测往返延迟 `rttMS` 与记忆/事件变化
interface SecondUpstreamTick {
    sec: number
    online: boolean
    intensity: number // 0.0 (停搏) | 0.28..1.00 (上游真实响应脉冲振幅)
    rttMS: number
    deltaUnits: number
    eventLabel: string
}

const WINDOW_SECONDS = 24
const MINI_SECONDS = 8

const adminInfo = useAdminInfo()
const info = ref<WidgetInfo | null>(null)
const telemetryHistory = ref<SecondUpstreamTick[]>([])
const lastTickFired = ref(false)
const lastTickLabel = ref('PROBING')

let wsSocket: MonitorSocket | null = null
let prevAssertions = -1
let prevEventTS = -1

const initHistory = () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const arr: SecondUpstreamTick[] = []
    for (let i = WINDOW_SECONDS - 1; i >= 0; i--) {
        arr.push({
            sec: nowSec - i,
            online: true,
            intensity: 0.42,
            rttMS: 1.2,
            deltaUnits: 0,
            eventLabel: 'OK',
        })
    }
    telemetryHistory.value = arr
}
initHistory()

// 每秒采样点由 status 流的 1Hz `probe` 帧驱动（服务端单一 watcher 采集、多标签页共享），
// 客户端不再每秒发一次 /widget 请求；`load()` 只在首屏与手动刷新时取一次性快照。
const applyProbeSample = (d: WidgetInfo | null) => {
    const nowSec = Math.floor(Date.now() / 1000)

    if (!d || !d.upstream_online) {
        lastTickFired.value = false
        lastTickLabel.value = isLocalMode.value ? 'LOCAL VAULT OFFLINE' : 'UPSTREAM OFFLINE'
        pushTick({
            sec: nowSec,
            online: false,
            intensity: 0,
            rttMS: 0,
            deltaUnits: 0,
            eventLabel: 'OFFLINE',
        })
        return
    }

    let deltaUnits = 0
    if (prevAssertions >= 0 && d.active_assertions !== prevAssertions) {
        deltaUnits = Math.abs(d.active_assertions - prevAssertions)
    }
    prevAssertions = d.active_assertions

    const evTS = d.last_event_ts || 0
    const hasNewHookEvent = prevEventTS >= 0 && evTS > prevEventTS
    prevEventTS = evTS

    const rtt = Number(d.upstream_rtt_ms) || 1.0
    // 将上游真实 RTT (ms) 与当秒事件增量映射为真实心电 R 峰脉冲高度：
    // - 正常在线每秒一次上游心跳基准脉冲 (0.38 ~ 0.65，随真实 RTT 细微起伏)；
    // - 若该秒伴随新检索/新记忆写入/后台任务，则激发高耸尖峰 (0.78 ~ 1.00)！
    let intensity = Math.min(0.68, 0.36 + (rtt % 12) * 0.025)
    let label = `RTT ${rtt.toFixed(1)}ms`

    if (hasNewHookEvent || deltaUnits > 0) {
        intensity = Math.min(1.0, 0.78 + Math.min(0.22, deltaUnits * 0.08 + rtt / 100))
        label = deltaUnits > 0 ? `+${deltaUnits} UNIT · ${rtt.toFixed(1)}ms` : `${d.last_event} · ${rtt.toFixed(1)}ms`
        lastTickFired.value = true
    } else if (d.bg_task) {
        intensity = 0.85
        label = `${d.bg_task} · ${rtt.toFixed(1)}ms`
        lastTickFired.value = true
    } else {
        lastTickFired.value = !lastTickFired.value
    }

    lastTickLabel.value = label
    pushTick({
        sec: nowSec,
        online: true,
        intensity,
        rttMS: rtt,
        deltaUnits,
        eventLabel: label,
    })
}

const pushTick = (tick: SecondUpstreamTick) => {
    const next = telemetryHistory.value.slice(-(WINDOW_SECONDS - 1))
    next.push(tick)
    telemetryHistory.value = next
}

// 将每秒真实上游采样序列渲染为连续的医疗心电图 P-Q-R-S-T 路径
const buildRollingEcgFromTicks = (
    ticks: SecondUpstreamTick[],
    width: number,
    baselineY: number,
    maxRPeakUp: number,
    maxSValleyDown: number,
    padRight = 5
): { path: string; headX: number; headY: number } => {
    const n = ticks.length
    if (n === 0) {
        return { path: `M 0 ${baselineY} L ${width} ${baselineY}`, headX: width - padRight, headY: baselineY }
    }
    const usableW = width - padRight
    const slotW = usableW / n
    const cmds: string[] = [`M 0 ${baselineY.toFixed(1)}`]
    let lastX = usableW
    let lastY = baselineY

    ticks.forEach((tk, idx) => {
        const x0 = idx * slotW
        const xEnd = (idx + 1) * slotW
        if (!tk.online || tk.intensity <= 0.02) {
            cmds.push(`L ${xEnd.toFixed(1)} ${baselineY.toFixed(1)}`)
            lastX = xEnd
            lastY = baselineY
            return
        }
        const amp = Math.max(0.22, Math.min(1.0, tk.intensity))
        const pUp = baselineY - amp * maxRPeakUp * 0.18
        const qDown = baselineY + amp * maxSValleyDown * 0.35
        const rUp = baselineY - amp * maxRPeakUp
        const sDown = baselineY + amp * maxSValleyDown
        const tUp = baselineY - amp * maxRPeakUp * 0.26

        const xPStart = x0 + slotW * 0.14
        const xPMid = x0 + slotW * 0.24
        const xPEnd = x0 + slotW * 0.34
        const xQ = x0 + slotW * 0.44
        const xR = x0 + slotW * 0.52
        const xS = x0 + slotW * 0.6
        const xJ = x0 + slotW * 0.68
        const xTMid = x0 + slotW * 0.8
        const xTEnd = x0 + slotW * 0.9

        cmds.push(
            `L ${xPStart.toFixed(1)} ${baselineY.toFixed(1)}`,
            `Q ${xPMid.toFixed(1)} ${pUp.toFixed(1)}, ${xPEnd.toFixed(1)} ${baselineY.toFixed(1)}`,
            `L ${xQ.toFixed(1)} ${qDown.toFixed(1)}`,
            `L ${xR.toFixed(1)} ${rUp.toFixed(1)}`,
            `L ${xS.toFixed(1)} ${sDown.toFixed(1)}`,
            `L ${xJ.toFixed(1)} ${baselineY.toFixed(1)}`,
            `Q ${xTMid.toFixed(1)} ${tUp.toFixed(1)}, ${xTEnd.toFixed(1)} ${baselineY.toFixed(1)}`,
            `L ${xEnd.toFixed(1)} ${baselineY.toFixed(1)}`
        )
        lastX = xEnd
        lastY = baselineY
    })

    return {
        path: cmds.join(' '),
        headX: Number(lastX.toFixed(1)),
        headY: Number(lastY.toFixed(1)),
    }
}

const miniRollingEcgPath = computed(() => buildRollingEcgFromTicks(telemetryHistory.value.slice(-MINI_SECONDS), 60, 10.5, 8.8, 4.2, 3))

const tipRollingEcgPath = computed(() => buildRollingEcgFromTicks(telemetryHistory.value, 264, 34, 27.5, 14.5, 5))

const load = async () => {
    try {
        const res = await request<WidgetInfo>(
            { url: '/admin/plugin/rolling-memory/widget', method: 'POST', data: {} },
            { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
        )
        const d = res.data.data ?? null
        if (d && d.upstream_online === undefined) {
            d.upstream_online = d.online
        }
        info.value = d
    } catch {
        info.value = null
    }
}

const stopAll = () => {
    if (wsSocket) {
        wsSocket.stop()
        wsSocket = null
    }
}

const startWS = (token: string) => {
    stopAll()
    wsSocket = new MonitorSocket({
        token,
        room: 'rm/status/probe',
        onMessage: (env: any) => {
            const event = env?.event
            const payload = env?.data
            if (!event || payload == null) return
            if (event === 'probe') {
                const d = payload as WidgetInfo
                if (info.value) {
                    Object.assign(info.value, d)
                }
                applyProbeSample(d)
                return
            }
            if (event === 'status') {
                const d = payload
                ;(window as any).__rmLatestStatus = d
                window.dispatchEvent(new CustomEvent('mem:status-stream', { detail: d }))
                if (info.value) {
                    const hookEv =
                        d.recent_hook_events?.[0]?.event || (d.hook_events?.[0] ? `${d.hook_events[0].event} (${d.hook_events[0].outcome})` : 'idle')
                    info.value.active_assertions = d.active_assertions ?? info.value.active_assertions
                    info.value.last_event = hookEv
                    info.value.last_event_ts = d.hook_events?.[0]?.ts ?? info.value.last_event_ts
                }
            }
        },
    })
    wsSocket.start()
}

const resumeIfVisible = () => {
    stopAll()
    const token = adminInfo.token
    if (!token) {
        info.value = null
        return
    }
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return
    }
    // 首屏取一次性快照补齐全静态字段（版本/模式/平台等），随后全部实时字段由 WebSocket 帧驱动。
    void load()
    startWS(token)
}

const onVisibilityChange = () => {
    if (document.visibilityState === 'hidden') {
        stopAll()
    } else {
        resumeIfVisible()
    }
}

if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', onVisibilityChange)
    window.addEventListener('beforeunload', stopAll)
    window.addEventListener('pagehide', stopAll)
}

watch(
    () => adminInfo.token,
    () => {
        resumeIfVisible()
    },
    { immediate: true }
)

onBeforeUnmount(() => {
    if (typeof document !== 'undefined') {
        document.removeEventListener('visibilitychange', onVisibilityChange)
        window.removeEventListener('beforeunload', stopAll)
        window.removeEventListener('pagehide', stopAll)
    }
    stopAll()
})
</script>

<style>
.rm-widget-popper .rm-widget-tip {
    max-width: 360px;
    line-height: 1.5;
}

.rm-widget-popper .rm-widget-tip-title {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 4px;
    font-weight: 600;
}

.rm-widget-popper .mode-indicator {
    padding: 1px 6px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 9px;
    font-weight: 700;
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    border-radius: 4px;
    border: 1px solid rgba(64, 158, 255, 0.2);
}

.rm-widget-popper .ecg-bpm-tag {
    padding: 1px 6px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 10px;
    font-weight: 700;
    color: #34d399;
    background: rgba(16, 185, 129, 0.16);
    border: 1px solid rgba(52, 211, 153, 0.35);
    border-radius: 4px;
}

.rm-widget-popper .ecg-bpm-tag.offline {
    color: #f87171;
    background: rgba(239, 68, 68, 0.16);
    border-color: rgba(248, 113, 113, 0.35);
}

.rm-widget-popper .rm-widget-ecg-screen {
    position: relative;
    padding: 4px 6px 14px;
    margin: 6px 0 8px;
    background: #061310;
    border: 1px solid rgba(16, 185, 129, 0.38);
    border-radius: 6px;
    box-shadow: inset 0 0 12px rgba(16, 185, 129, 0.12);
}

.rm-widget-popper .rm-widget-ecg-screen.offline {
    background: #140c0c;
    border-color: rgba(239, 68, 68, 0.35);
}

.rm-widget-popper .rm-tip-ecg-svg {
    display: block;
    width: 264px;
    height: 54px;
}

.rm-widget-popper .ecg-iso-line {
    stroke: rgba(52, 211, 153, 0.22);
    stroke-width: 0.7;
    stroke-dasharray: 3 3;
}

.rm-widget-popper .ecg-live-trace {
    fill: none;
    stroke: #34d399;
    stroke-width: 1.75;
    stroke-linecap: round;
    stroke-linejoin: round;
    filter: drop-shadow(0 0 3px rgba(52, 211, 153, 0.7));
}

.rm-widget-popper .rm-widget-ecg-screen.offline .ecg-live-trace {
    stroke: #ef4444;
    filter: none;
}

.rm-widget-popper .ecg-playhead-dot {
    fill: #a7f3d0;
    stroke: #10b981;
    stroke-width: 1.2;
    filter: drop-shadow(0 0 4px #34d399);
}

.rm-widget-popper .ecg-lead-label {
    position: absolute;
    right: 8px;
    bottom: 2px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 9px;
    color: rgba(110, 231, 183, 0.65);
    letter-spacing: 0.04em;
}

.rm-widget-popper .rm-widget-tip-meta {
    margin-top: 4px;
    font-size: 12px;
    opacity: 0.78;
}

.rm-widget-popper .rm-widget-tip-action {
    margin-top: 4px;
    font-size: 11px;
    font-weight: 600;
    color: #89b4fa;
}
</style>

<style scoped>
.rm-widget-badge {
    position: fixed;
    right: 16px;
    bottom: 14px;
    z-index: 1500;
    display: inline-flex;
    gap: 6px;
    align-items: center;
    height: 24px;
    padding: 0 10px;
    color: var(--el-color-success);
    font-size: 11px;
    font-weight: 700;
    line-height: 1;
    letter-spacing: 0.04em;
    cursor: pointer;
    user-select: none;
    background: var(--el-color-success-light-9);
    border: 1px solid var(--el-color-success-light-5);
    border-radius: 999px;
    box-shadow: var(--el-box-shadow-lighter);
    transition:
        transform 0.2s ease,
        box-shadow 0.2s ease;
}

.rm-widget-badge:hover {
    transform: translateY(-1px);
    box-shadow: var(--el-box-shadow-light);
}

.rm-widget-badge.is-offline {
    color: var(--el-color-info);
    background: var(--el-color-info-light-9);
    border-color: var(--el-color-info-light-5);
}

.rm-widget-dot {
    width: 6px;
    height: 6px;
    background: currentColor;
    border-radius: 50%;
    transition: transform 0.2s ease;
}

.rm-widget-dot.beating {
    transform: scale(1.35);
}

.rm-mini-ecg-box {
    display: inline-flex;
    align-items: center;
    padding: 0 3px;
    background: rgba(6, 19, 16, 0.9);
    border: 1px solid rgba(16, 185, 129, 0.42);
    border-radius: 4px;
}

.rm-widget-mode-chip {
    padding: 2px 6px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 9.5px;
    font-weight: 800;
    line-height: 1;
    color: #ffffff;
    background: linear-gradient(135deg, #10b981, #059669);
    border-radius: 999px;
    box-shadow: 0 1px 4px rgba(16, 185, 129, 0.35);
}

.rm-widget-badge.is-offline .rm-mini-ecg-box {
    background: rgba(30, 41, 59, 0.65);
    border-color: rgba(148, 163, 184, 0.35);
}

.rm-widget-mini-ecg {
    width: 60px;
    height: 15px;
    overflow: visible;
}

.mini-ecg-axis {
    stroke: rgba(52, 211, 153, 0.22);
    stroke-width: 0.6;
    stroke-dasharray: 2 2;
}

.mini-ecg-trace {
    fill: none;
    stroke: #34d399;
    stroke-width: 1.35;
    stroke-linecap: round;
    stroke-linejoin: round;
    filter: drop-shadow(0 0 2px rgba(52, 211, 153, 0.8));
}

.rm-widget-badge.is-offline .mini-ecg-trace {
    stroke: #94a3b8;
    filter: none;
}

.ecg-playhead-dot {
    fill: #a7f3d0;
    stroke: #10b981;
    stroke-width: 0.9;
}
</style>
