<template>
    <div>
        <el-dialog
            v-model="terminal.state.show"
            :title="isEn ? 'Rolling Memory Live CLI Terminal' : 'Rolling Memory 实时命令行终端'"
            class="ba-terminal-dialog main-dialog"
            append-to-body
            @closed="terminal.stopStream()"
        >
            <!-- 顶部实时命令行监控流（tail -f rolling-memory.log + 写入断言回显） -->
            <div class="live-cli-box">
                <div class="live-cli-header">
                    <div class="cli-dots">
                        <span class="dot red"></span>
                        <span class="dot yellow"></span>
                        <span class="dot green"></span>
                        <span class="cli-prompt">
                            rolling-memory@{{ terminal.state.mode }}:~ $ tail -f ~/.local/state/rolling-memory/rolling-memory.log
                        </span>
                    </div>
                    <div class="cli-actions">
                        <span class="cli-status" :class="{ paused: terminal.state.paused }">
                            <i class="pulse-dot"></i>
                            {{ terminal.state.paused ? (isEn ? 'PAUSED' : '已暂停') : 'LIVE WS · ' + terminal.state.updatedAt }}
                        </span>
                        <el-radio-group v-model="terminal.state.filterTag" size="small" class="cli-filter">
                            <el-radio-button value="ALL">ALL</el-radio-button>
                            <el-radio-button value="HOOK">HOOK</el-radio-button>
                            <el-radio-button value="RAME">RAME</el-radio-button>
                        </el-radio-group>
                        <el-button size="small" text class="cli-btn" @click="terminal.togglePause()">
                            {{ terminal.state.paused ? (isEn ? 'Resume' : '继续') : isEn ? 'Pause' : '暂停' }}
                        </el-button>
                        <el-button size="small" text class="cli-btn" @click="terminal.fetchStream()">
                            {{ isEn ? 'Refresh' : '刷新' }}
                        </el-button>
                    </div>
                </div>
                <div class="live-cli-body">
                    <div v-if="filteredStreamLines.length === 0" class="cli-line muted">
                        [{{ terminal.state.updatedAt }}] Waiting for Hook / Memory events...
                    </div>
                    <div v-for="line in filteredStreamLines" :key="line.id" class="cli-line">
                        <span class="cli-ts">[{{ line.time_text }}]</span>
                        <span class="cli-tag" :class="'lvl-' + line.level">[{{ line.tag }}]</span>
                        <span class="cli-text">{{ line.text }}</span>
                    </div>
                </div>
            </div>

            <!-- BuildAdmin 经典任务时间线 -->
            <el-scrollbar ref="terminalScrollbarRef" :max-height="340" class="terminal-scrollbar">
                <el-timeline class="terminal-timeline" v-if="terminal.state.taskList.length">
                    <el-timeline-item
                        v-for="(item, idx) in terminal.state.taskList"
                        :key="item.uuid"
                        class="task-item"
                        :class="'task-status-' + item.status"
                        :type="getTaskStatus(item.status).statusType"
                        center
                        :timestamp="item.createTime"
                        placement="top"
                    >
                        <el-card shadow="never">
                            <div class="task-head-row">
                                <el-tag size="small" :type="getTaskStatus(item.status).statusType">
                                    {{ getTaskStatus(item.status).statusText }}
                                </el-tag>
                                <span class="command">{{ item.command }}</span>
                                <div class="task-opt">
                                    <el-button
                                        :title="isEn ? 'Re-run' : '重新执行'"
                                        size="small"
                                        v-blur
                                        type="warning"
                                        circle
                                        @click="terminal.retryTask(idx)"
                                    >
                                        <Icon name="el-refresh-right" :size="12" />
                                    </el-button>
                                    <el-button
                                        :title="isEn ? 'Delete' : '删除'"
                                        size="small"
                                        v-blur
                                        type="danger"
                                        circle
                                        @click="terminal.delTask(idx)"
                                    >
                                        <Icon name="el-delete" :size="12" />
                                    </el-button>
                                </div>
                            </div>
                            <template v-if="item.status !== taskStatus.Waiting">
                                <div
                                    v-if="item.status !== taskStatus.Connecting && item.status !== taskStatus.Executing"
                                    @click="terminal.setTaskShowMessage(idx)"
                                    class="toggle-message-display"
                                >
                                    <span>{{ isEn ? 'Command run log' : '命令运行日志' }}</span>
                                    <Icon :name="item.showMessage ? 'el-arrow-up' : 'el-arrow-down'" :size="14" color="#909399" />
                                </div>
                                <div
                                    v-if="
                                        item.status === taskStatus.Connecting ||
                                        item.status === taskStatus.Executing ||
                                        (item.status > taskStatus.Executing && item.showMessage)
                                    "
                                    class="exec-message"
                                    :class="'exec-message-' + item.uuid"
                                >
                                    <pre v-for="(msg, index) in item.message" :key="index" class="message-item">{{ msg }}</pre>
                                </div>
                            </template>
                        </el-card>
                    </el-timeline-item>
                </el-timeline>
                <el-empty
                    v-else
                    :image-size="60"
                    :description="isEn ? 'Click a CLI command below to run live inspection' : '点击下方快捷命令按钮即可执行引擎 CLI 实时诊断'"
                />
            </el-scrollbar>

            <!-- BuildAdmin 底部快捷命令按钮栏 -->
            <div class="terminal-buttons">
                <el-button class="terminal-menu-item" type="primary" plain v-blur @click="runCmd('status', 'rolling-memory status --json')">
                    <Icon name="lucide-terminal" :size="14" style="margin-right: 5px" />
                    rmem status
                </el-button>
                <el-button class="terminal-menu-item" type="success" plain v-blur @click="runCmd('doctor', 'rolling-memory doctor --json')">
                    <Icon name="el-circle-check" :size="14" style="margin-right: 5px" />
                    rmem doctor
                </el-button>
                <el-button class="terminal-menu-item" v-blur @click="runCmd('recent', 'rolling-memory recent --limit 10 --json')">
                    <Icon name="el-clock" :size="14" style="margin-right: 5px" />
                    rmem recent
                </el-button>
                <el-dropdown class="terminal-menu-item">
                    <el-button v-blur>
                        <Icon name="el-cpu" :size="14" style="margin-right: 5px" />
                        {{ isEn ? 'More CLI Commands' : '更多诊断命令' }}
                    </el-button>
                    <template #dropdown>
                        <el-dropdown-menu>
                            <el-dropdown-item @click="runCmd('check', 'rolling-memory check --json')">
                                rolling-memory check --json
                            </el-dropdown-item>
                        </el-dropdown-menu>
                    </template>
                </el-dropdown>
                <el-button class="terminal-menu-item" v-blur @click="terminal.clearSuccessTask()">
                    <Icon name="el-delete" :size="14" style="margin-right: 5px" />
                    {{ isEn ? 'Clean completed' : '清理已完成任务' }}
                </el-button>
            </div>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import { useConfig } from '@/stores/config'
import { taskStatus, useTerminal } from '@/stores/terminal'
import type { TimelineItemProps } from 'element-plus'
import { ElScrollbar } from 'element-plus'
import { computed, nextTick, onBeforeUnmount, onMounted, useTemplateRef } from 'vue'

const config = useConfig()
const terminal = useTerminal()
const terminalScrollbarRef = useTemplateRef('terminalScrollbarRef')
const isEn = computed(() => config.lang.active === 'en')

const filteredStreamLines = computed(() => {
    const tag = terminal.state.filterTag
    if (tag === 'ALL') return terminal.state.streamLines
    if (tag === 'HOOK') return terminal.state.streamLines.filter((l) => l.tag.startsWith('HOOK:'))
    return terminal.state.streamLines.filter((l) => l.tag.startsWith('RAME:'))
})

const getTaskStatus = (status: number) => {
    let statusText = isEn.value ? 'Unknown' : '未知'
    let statusType: TimelineItemProps['type'] = 'info'
    switch (status) {
        case taskStatus.Waiting:
            statusText = isEn.value ? 'Waiting' : '等待执行'
            statusType = 'info'
            break
        case taskStatus.Connecting:
            statusText = isEn.value ? 'Connecting' : '连接中'
            statusType = 'warning'
            break
        case taskStatus.Executing:
            statusText = isEn.value ? 'Executing' : '正在执行'
            statusType = 'warning'
            break
        case taskStatus.Success:
            statusText = isEn.value ? 'Completed' : '执行成功'
            statusType = 'success'
            break
        case taskStatus.Failed:
            statusText = isEn.value ? 'Failed' : '执行失败'
            statusType = 'danger'
            break
        case taskStatus.Unknown:
            statusText = isEn.value ? 'Unknown' : '执行结果未知'
            statusType = 'danger'
            break
    }
    return {
        statusText,
        statusType,
    }
}

const runCmd = (cmdKey: string, label: string) => {
    terminal.addTask(cmdKey, label)
    nextTick(() => {
        if (terminalScrollbarRef.value && terminalScrollbarRef.value.wrapRef) {
            terminalScrollbarRef.value.setScrollTop(0)
        }
    })
}

const handleOpenEvent = () => {
    terminal.toggle(true)
}

onMounted(() => {
    terminal.init()
    window.addEventListener('mem:open-terminal', handleOpenEvent)
})

onBeforeUnmount(() => {
    terminal.stopStream()
    window.removeEventListener('mem:open-terminal', handleOpenEvent)
})
</script>

<style scoped lang="scss">
.live-cli-box {
    margin-bottom: 14px;
    border-radius: 8px;
    overflow: hidden;
    border: 1px solid #2e303e;
    background: #181924;
    font-family: 'JetBrains Mono', 'SFMono-Regular', Consolas, Menlo, monospace;
}

.live-cli-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
    padding: 8px 12px;
    background: #232534;
    border-bottom: 1px solid #2e303e;
}

.cli-dots {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    .dot {
        width: 10px;
        height: 10px;
        border-radius: 50%;
        &.red {
            background: #ff5f56;
        }
        &.yellow {
            background: #ffbd2e;
        }
        &.green {
            background: #27c93f;
        }
    }
    .cli-prompt {
        margin-left: 6px;
        font-size: 12px;
        color: #cdd6f4;
        font-weight: 600;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
}

.cli-actions {
    display: flex;
    align-items: center;
    gap: 8px;
}

.cli-status {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11px;
    color: #a6e3a1;
    .pulse-dot {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: #a6e3a1;
        box-shadow: 0 0 6px #a6e3a1;
    }
    &.paused {
        color: #f9e2af;
        .pulse-dot {
            background: #f9e2af;
            box-shadow: none;
        }
    }
}

.cli-btn {
    color: #89b4fa !important;
    padding: 2px 6px !important;
}

.live-cli-body {
    max-height: 210px;
    overflow-y: auto;
    padding: 10px 12px;
    font-size: 12px;
    line-height: 1.65;
    color: #cdd6f4;
}

.cli-line {
    display: flex;
    align-items: baseline;
    gap: 8px;
    white-space: pre-wrap;
    word-break: break-all;
    &.muted {
        color: #6c7086;
    }
}

.cli-ts {
    color: #7f849c;
    flex-shrink: 0;
}

.cli-tag {
    flex-shrink: 0;
    font-weight: 700;
    &.lvl-success {
        color: #a6e3a1;
    }
    &.lvl-primary {
        color: #89b4fa;
    }
    &.lvl-warning {
        color: #f9e2af;
    }
    &.lvl-info {
        color: #94e2d5;
    }
}

.cli-text {
    color: #e6e9ef;
}

.terminal-timeline {
    padding: 4px 12px 0;
}
.task-head-row {
    display: flex;
    align-items: center;
    gap: 8px;
}
.command {
    font-size: 13px;
    font-weight: 700;
    font-family: 'JetBrains Mono', Consolas, monospace;
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.exec-message {
    color: #e5eaf3;
    font-size: 12px;
    line-height: 1.55;
    font-family: 'JetBrains Mono', Consolas, monospace;
    padding: 8px 10px;
    background-color: #242636;
    border-radius: 6px;
    margin-top: 8px;
    min-height: 30px;
    max-height: 200px;
    overflow: auto;
}
.message-item {
    margin: 0;
    white-space: pre-wrap;
    word-break: break-word;
}
.toggle-message-display {
    padding-top: 8px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 4px;
    cursor: pointer;
}
.task-opt {
    display: inline-flex;
    gap: 4px;
}
.terminal-menu-item {
    margin-bottom: 10px;
}
.terminal-menu-item + .terminal-menu-item {
    margin-left: 10px;
    margin-bottom: 10px;
}
.terminal-buttons {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: center;
    padding-top: 12px;
}
:deep(.main-dialog) {
    --el-dialog-padding-primary: 16px 18px 8px 18px;
    .el-dialog__body {
        margin-top: 10px;
    }
}
:deep(.ba-terminal-dialog) {
    --el-dialog-width: 62% !important;
}
@media screen and (max-width: 960px) {
    :deep(.ba-terminal-dialog) {
        --el-dialog-width: 88% !important;
    }
}
</style>
