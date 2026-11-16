<!--
  图谱下钻出口组件（下钻面包屑 + 下钻态势卡 + 提示行）—— 单源实现，首页 `views/home/MemoryField.vue`
  与后台 `views/admin/dashboard/graph.vue` 两处共用；样式与文案在此维护，杜绝两处漂移。
  - 三条返回路径：面包屑根「全景星系」/ 态势卡「退出」按钮 / Esc（Esc 由宿主的键盘处理调用 exitDrill）。
  - tone：`dark` = 首页星舰驾驶舱暗色玻璃；`light` = 后台控制台 Element 亮色。
  - topPx：各页 HUD 行基线（首页 58、后台 10），槽位逻辑一致：面包屑居中、态势卡占下钻态让出的召回面板槽位。
-->
<script setup lang="ts">
import Icon from '@/components/icon/index.vue'

const props = withDefaults(
    defineProps<{
        /** 是否处于下钻态（宿主的 inDrill） */
        visible: boolean
        /** 下钻面包屑栈（根 → 当前） */
        crumbs: { key: string; summary: string; id: number }[]
        /** 下钻焦点节点（用于态势卡标题） */
        focus: { summary: string } | null
        /** 当前跳数（1 | 2 | 3） */
        depth: number
        /** 焦点一阵统计（出/入边与 1/2/3 跳计数） */
        ego: { out: number; in: number; hop1: number; hop2: number; hop3: number } | null
        isEn: boolean
        tone?: 'dark' | 'light'
        topPx?: number
    }>(),
    { tone: 'dark', topPx: 10 }
)

const emit = defineEmits<{
    (e: 'exit'): void
    (e: 'pop-to', index: number): void
    (e: 'set-depth', depth: 1 | 2 | 3): void
}>()

const hops: Array<1 | 2 | 3> = [1, 2, 3]

function clip(s: string, len: number): string {
    const one = (s || '').replace(/\s+/g, ' ').trim()
    return one.length > len ? `${one.slice(0, len)}…` : one
}
</script>

<template>
    <div v-if="props.visible" class="gdo gdo--breadcrumb" :class="`gdo--${props.tone}`" :style="{ top: `${props.topPx}px` }">
        <span class="bc-crumb bc-crumb--root" @click="emit('exit')">
            <Icon name="lucide-globe" :size="13" />
            {{ props.isEn ? 'Overview Galaxy' : '全景星系' }}
        </span>
        <template v-for="(c, i) in props.crumbs" :key="c.key + i">
            <span class="bc-sep">›</span>
            <span class="bc-crumb" :class="{ 'is-current': i === props.crumbs.length - 1 }" @click="emit('pop-to', i)">
                {{ clip(c.summary, 26) }}
            </span>
        </template>
        <span class="bc-spacer" />
        <span class="bc-depth-label">{{ props.isEn ? 'Depth' : '跳数' }}</span>
        <span class="bc-depth-group">
            <button
                v-for="d in hops"
                :key="d"
                type="button"
                class="bc-depth-btn"
                :class="{ 'is-on': props.depth === d }"
                @click="emit('set-depth', d)"
            >
                {{ d }}-Hop
            </button>
        </span>
        <el-button size="small" plain @click="emit('exit')">
            {{ props.isEn ? 'Exit (Esc)' : '退出下钻 (Esc)' }}
        </el-button>
    </div>

    <div v-if="props.visible && props.focus" class="gdo gdo--card" :class="`gdo--${props.tone}`" :style="{ top: `${props.topPx}px` }">
        <div class="hud-drill-head">
            <span class="hud-drill-pulse" />
            <span class="hud-drill-title">{{ props.isEn ? 'Drill-down Focus' : '独立下钻焦点' }}</span>
            <span v-if="props.ego" class="hud-drill-deg">
                {{ props.isEn ? 'Out' : '出边' }} {{ props.ego.out }} · {{ props.isEn ? 'In' : '入边' }} {{ props.ego.in }}
            </span>
        </div>
        <div class="hud-drill-node" :title="props.focus.summary">{{ clip(props.focus.summary, 46) }}</div>
        <div v-if="props.ego" class="hud-drill-meta">
            <span class="hdm-item"
                ><i class="hdm-dot" style="background: #409eff" />{{ props.isEn ? '1-Hop' : '1 跳环' }} <b>{{ props.ego.hop1 }}</b></span
            >
            <span class="hdm-item"
                ><i class="hdm-dot" style="background: #06b6d4" />{{ props.isEn ? '2-Hop' : '2 跳环' }} <b>{{ props.ego.hop2 }}</b></span
            >
            <span class="hdm-item"
                ><i class="hdm-dot" style="background: #94a3b8" />{{ props.isEn ? '3-Hop' : '3 跳环' }} <b>{{ props.ego.hop3 }}</b></span
            >
        </div>
        <div class="hud-drill-actions">
            <span class="hud-depth-group">
                <button
                    v-for="d in hops"
                    :key="d"
                    type="button"
                    class="bc-depth-btn"
                    :class="{ 'is-on': props.depth === d }"
                    @click="emit('set-depth', d)"
                >
                    {{ d }}-Hop
                </button>
            </span>
            <el-button size="small" text type="warning" @click="emit('exit')">
                {{ props.isEn ? 'Exit (Esc)' : '退出 (Esc)' }}
            </el-button>
        </div>
        <div class="hud-drill-tip">
            {{ props.isEn ? 'Double-click any neighbor to re-center the drill-down on it.' : '双击环上任一邻居，即可把它接力设为新的下钻中心。' }}
        </div>
    </div>
</template>

<style scoped lang="scss">
.gdo {
    position: absolute;
    z-index: 9;
}

.gdo--breadcrumb {
    left: 50%;
    transform: translateX(-50%);
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    padding: 6px 10px;
}

.gdo--card {
    right: 10px;
    width: 268px;
    padding: 8px 10px;
    border-radius: 10px;
}
// 面包屑内容
.bc-crumb {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    max-width: 220px;
    padding: 2px 8px;
    font-size: 12px;
    cursor: pointer;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    transition: all 0.16s ease;

    &--root {
        font-weight: 600;
    }

    &.is-current {
        font-weight: 700;
    }
}

.bc-spacer {
    flex: 1 1 auto;
}

.bc-depth-label {
    font-size: 11px;
}

.bc-depth-group,
.hud-depth-group {
    display: inline-flex;
    align-items: center;
    overflow: hidden;
}

.bc-depth-btn {
    padding: 2px 9px;
    border: none;
    font-size: 11px;
    font-weight: 600;
    background: transparent;
    cursor: pointer;
    transition: all 0.16s ease;
}

// 态势卡内容
.hud-drill-head {
    display: flex;
    align-items: center;
    gap: 6px;
}

.hud-drill-pulse {
    width: 7px;
    height: 7px;
    border-radius: 50%;
}

.hud-drill-deg {
    margin-left: auto;
    font-size: 10px;
    font-weight: 600;
}

.hud-drill-node {
    margin: 6px 0 5px;
    font-size: 12px;
    font-weight: 600;
    line-height: 1.35;
}

.hud-drill-meta {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 10.5px;

    b {
        margin-left: 2px;
        font-weight: 700;
    }
}

.hdm-item {
    display: inline-flex;
    align-items: center;
    gap: 4px;
}

.hdm-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
}

.hud-drill-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 6px;
    margin-top: 8px;
}

.hud-drill-tip {
    margin-top: 5px;
    font-size: 11px;
    line-height: 1.5;
}

// ===== 首页：星舰驾驶舱暗色玻璃 =====
.gdo--dark {
    border: 1px solid rgba(56, 189, 248, 0.3);
    color: #e2e8f0;
    backdrop-filter: blur(12px);
    box-shadow: 0 8px 28px rgba(0, 2, 10, 0.65);

    &.gdo--breadcrumb {
        background: rgba(6, 14, 28, 0.82);
        border-radius: 999px;
        // 首页画布为 100vw，两侧让出左枢纽榜与右让位卡的位置
        max-width: min(560px, calc(100vw - 700px));
    }

    &.gdo--card {
        right: 14px;
        background: rgba(6, 14, 28, 0.78);
    }

    .bc-crumb {
        color: #cbd5e1;
        border-radius: 999px;

        &:hover {
            background: rgba(56, 189, 248, 0.14);
            color: #f1f5f9;
        }

        &--root {
            color: #38bdf8;
        }

        &.is-current {
            color: #fde68a;
            background: rgba(251, 191, 36, 0.16);
        }
    }

    .bc-sep {
        color: #64748b;
    }

    .bc-depth-label {
        color: #94a3b8;
    }

    .bc-depth-group,
    .hud-depth-group {
        border: 1px solid rgba(56, 189, 248, 0.28);
        border-radius: 999px;
    }

    .bc-depth-btn {
        color: #cbd5e1;

        & + .bc-depth-btn {
            border-left: 1px solid rgba(56, 189, 248, 0.18);
        }

        &:hover {
            color: #fde68a;
            background: rgba(251, 191, 36, 0.14);
        }

        &.is-on {
            color: #0b1220;
            background: #fbbf24;
        }
    }

    .hud-drill-pulse {
        background: #fbbf24;
        box-shadow: 0 0 8px rgba(251, 191, 36, 0.85);
    }

    .hud-drill-deg,
    .hud-drill-meta,
    .hud-drill-tip {
        color: #94a3b8;
    }

    .hud-drill-title {
        font-size: 11.5px;
        font-weight: 700;
        color: #fbbf24;
    }

    .hud-drill-node {
        color: #f1f5f9;
    }

    .hud-drill-meta b {
        color: #e2e8f0;
    }

    :deep(.el-button) {
        height: 22px;
        padding: 0 10px;
        border-color: rgba(56, 189, 248, 0.35);
        background: rgba(56, 189, 248, 0.12);
        color: #e2e8f0;

        &:hover {
            border-color: rgba(56, 189, 248, 0.6);
            background: rgba(56, 189, 248, 0.22);
            color: #f8fafc;
        }
    }
}

// ===== 后台控制台：Element 亮色 =====
.gdo--light {
    &.gdo--breadcrumb {
        border: 1px solid var(--el-color-warning-light-5);
        border-radius: 8px;
        background: var(--el-color-warning-light-9);
        // 后台画布在卡片容器内，两侧让出左枢纽榜与右让位卡的位置
        max-width: min(560px, calc(100% - 560px));
    }
    &.gdo--card {
        border: 1px solid var(--el-color-warning-light-5);
        background: var(--el-bg-color-overlay);
        box-shadow: var(--el-box-shadow-light);
        backdrop-filter: blur(6px);
    }

    .bc-crumb {
        color: var(--el-text-color-regular);
        border-radius: 6px;

        &:hover {
            background: var(--el-fill-color);
            color: var(--el-text-color-primary);
        }

        &--root {
            color: var(--el-color-primary);
        }

        &.is-current {
            color: var(--el-color-warning-dark-2);
            background: var(--el-color-warning-light-8);
        }
    }

    .bc-sep {
        color: var(--el-text-color-placeholder);
    }

    .bc-depth-label {
        color: var(--el-text-color-secondary);
    }

    .bc-depth-group,
    .hud-depth-group {
        border: 1px solid var(--el-border-color);
        border-radius: 6px;
    }

    .bc-depth-btn {
        color: var(--el-text-color-regular);
        background: var(--el-bg-color);

        & + .bc-depth-btn {
            border-left: 1px solid var(--el-border-color-lighter);
        }

        &:hover {
            color: var(--el-color-warning-dark-2);
            background: var(--el-color-warning-light-9);
        }

        &.is-on {
            color: #ffffff;
            background: var(--el-color-warning);
        }
    }

    .hud-drill-pulse {
        width: 8px;
        height: 8px;
        background: var(--el-color-warning);
        box-shadow: 0 0 0 0 rgba(230, 162, 60, 0.55);
        animation: hud-drill-pulse 2s ease-out infinite;
    }

    .hud-drill-title {
        font-size: 12px;
        font-weight: 700;
        color: var(--el-color-warning-dark-2);
    }

    .hud-drill-deg {
        font-size: 11px;
        color: var(--el-text-color-secondary);
    }

    .hud-drill-node {
        color: var(--el-text-color-primary);
    }

    .hud-drill-meta {
        color: var(--el-text-color-secondary);

        b {
            color: var(--el-text-color-primary);
        }
    }

    .hud-drill-tip {
        color: var(--el-text-color-placeholder);
    }
}

@keyframes hud-drill-pulse {
    0% {
        box-shadow: 0 0 0 0 rgba(230, 162, 60, 0.55);
    }
    70% {
        box-shadow: 0 0 0 7px rgba(230, 162, 60, 0);
    }
    100% {
        box-shadow: 0 0 0 0 rgba(230, 162, 60, 0);
    }
}
</style>
