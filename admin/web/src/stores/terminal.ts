import { useAdminInfo } from '@/stores/adminInfo'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import { defineStore } from 'pinia'
import { nextTick, reactive } from 'vue'

export const taskStatus = {
    Waiting: 0,
    Connecting: 1,
    Executing: 2,
    Success: 3,
    Failed: 4,
    Unknown: 5,
} as const

export interface TerminalStreamLine {
    id: string
    time_text: string
    level: 'info' | 'success' | 'warning' | 'primary'
    tag: string
    text: string
}

export interface TerminalTaskItem {
    uuid: string
    createTime: string
    status: number
    command: string
    cmdKey: string
    message: string[]
    showMessage: boolean
}

interface TerminalApiPayload {
    online: boolean
    version: string
    mode: string
    scope: string
    binary_path: string
    db_path: string
    updated_at: string
    command: string
    exit_code: number
    output_lines: string[]
    stream_lines: TerminalStreamLine[]
}

const nowTimeStr = () => {
    const d = new Date()
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

export const useTerminal = defineStore('terminal', () => {
    const state = reactive({
        show: false,
        showDot: false,
        paused: false,
        autoScroll: true,
        filterTag: 'ALL' as 'ALL' | 'HOOK' | 'RAME',
        online: true,
        version: '0.0.1',
        mode: 'local',
        scope: 'mem:self',
        binaryPath: '',
        dbPath: '',
        updatedAt: '--:--:--',
        streamLines: [] as TerminalStreamLine[],
        taskList: [] as TerminalTaskItem[],
    })

    const adminInfo = useAdminInfo()
    let wsSocket: MonitorSocket | null = null
    let lastTopLineId = ''

    function applyStreamPayload(d: TerminalApiPayload) {
        state.online = d.online
        state.version = d.version || '0.0.1'
        state.mode = d.mode || 'local'
        state.scope = d.scope || 'mem:self'
        state.binaryPath = d.binary_path || ''
        state.dbPath = d.db_path || ''
        state.updatedAt = d.updated_at || '--:--:--'
        if (Array.isArray(d.stream_lines)) {
            const topId = d.stream_lines[0]?.id || ''
            if (lastTopLineId && topId && topId !== lastTopLineId && !state.show) {
                state.showDot = true
            }
            if (topId) {
                lastTopLineId = topId
            }
            state.streamLines = d.stream_lines
        }
    }

    // 一次性取值：首屏、打开抽屉、手动刷新与单步推进后的补帧。
    async function fetchStream() {
        if (state.paused) return
        try {
            const res = await request<TerminalApiPayload>(
                {
                    url: '/admin/plugin/rolling-memory/terminal',
                    method: 'POST',
                    data: { action: 'stream', limit: 50 },
                },
                { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
            )
            const d = res.data?.data
            if (!d) return
            applyStreamPayload(d)
        } catch {
            // 静默容错
        }
    }

    // 实时输出由同源 WebSocket 推送：服务端共享 watcher 在 Hook 日志/金库指纹变化时向 rm/terminal 房间广播。
    function startStream() {
        if (wsSocket) return
        const token = adminInfo.token
        if (!token) return
        wsSocket = new MonitorSocket({
            token,
            room: 'rm/terminal:50',
            onMessage: (env: any) => {
                if (state.paused) return
                if (env?.event === 'terminal' && env.data) {
                    applyStreamPayload(env.data as TerminalApiPayload)
                }
            },
        })
        wsSocket.start()
    }

    function stopStream() {
        if (wsSocket) {
            wsSocket.stop()
            wsSocket = null
        }
    }

    function init() {
        for (const item of state.taskList) {
            if (item.status === taskStatus.Connecting || item.status === taskStatus.Executing) {
                item.status = taskStatus.Unknown
            }
        }
        // 仅在终端弹窗打开时才订阅 rm/terminal WebSocket 房间并拉取快照，
        // 避免后台静默期无谓建立终端流订阅。
        if (state.show) {
            fetchStream()
            startStream()
        }
    }

    function toggle(val = !state.show) {
        state.show = val
        if (val) {
            state.showDot = false
            fetchStream()
            startStream()
        } else {
            stopStream()
        }
    }

    function toggleDot(val = !state.showDot) {
        state.showDot = val
    }

    function togglePause() {
        state.paused = !state.paused
        if (!state.paused) {
            fetchStream()
        }
    }

    function setTaskShowMessage(idx: number, val?: boolean) {
        const item = state.taskList[idx]
        if (!item) return
        item.showMessage = val !== undefined ? val : !item.showMessage
    }

    async function addTask(cmdKey: string, label?: string) {
        const id = `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
        const task: TerminalTaskItem = reactive({
            uuid: id,
            createTime: nowTimeStr(),
            status: taskStatus.Executing,
            command: label || `rolling-memory ${cmdKey}`,
            cmdKey,
            message: [`$ rolling-memory ${cmdKey} --json`, 'Executing command on local SQLite engine...'],
            showMessage: true,
        })
        state.taskList.unshift(task)

        try {
            const res = await request<TerminalApiPayload>(
                {
                    url: '/admin/plugin/rolling-memory/terminal',
                    method: 'POST',
                    data: { action: 'exec', command: cmdKey, limit: 50 },
                },
                { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
            )
            const d = res.data?.data
            if (d) {
                task.command = d.command || task.command
                task.message = [`$ ${task.command}`, ...(d.output_lines || [])]
                task.status = d.exit_code === 0 ? taskStatus.Success : taskStatus.Failed
                if (Array.isArray(d.stream_lines)) {
                    state.streamLines = d.stream_lines
                }
            } else {
                task.status = taskStatus.Failed
                task.message.push('[ERROR] Empty response from server')
            }
        } catch (e: any) {
            task.status = taskStatus.Failed
            task.message.push(`[ERROR] ${e?.message || 'Execution failed'}`)
        }

        if (!state.show) {
            state.showDot = true
        }
        await nextTick()
        const el = document.querySelector('.exec-message-' + task.uuid)
        if (el) {
            el.scrollTop = el.scrollHeight
        }
    }

    function retryTask(idx: number) {
        const item = state.taskList[idx]
        if (!item) return
        const cmdKey = item.cmdKey
        const cmdLabel = item.command
        state.taskList.splice(idx, 1)
        addTask(cmdKey, cmdLabel)
    }

    function delTask(idx: number) {
        const item = state.taskList[idx]
        if (!item) return
        if (item.status !== taskStatus.Connecting && item.status !== taskStatus.Executing) {
            state.taskList.splice(idx, 1)
        }
    }

    function clearSuccessTask() {
        state.taskList = state.taskList.filter((item) => item.status !== taskStatus.Success)
    }

    return {
        state,
        init,
        toggle,
        toggleDot,
        togglePause,
        fetchStream,
        startStream,
        stopStream,
        setTaskShowMessage,
        addTask,
        retryTask,
        delTask,
        clearSuccessTask,
    }
})
