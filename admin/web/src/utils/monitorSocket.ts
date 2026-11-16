import { getBaseURL } from '@/utils/request'

export type MonitorSocketStatus = 'connecting' | 'connected' | 'reconnecting' | 'disabled'

export interface MonitorSocketOptions {
    path?: string
    token: string
    room: string
    onMessage: (data: any) => void
    onStatus?: (status: MonitorSocketStatus) => void
    onResync?: () => void
}

/** 默认 WS 路径与退避区间（1s → 10s） */
const DEFAULT_PATH = '/ws'
const MIN_RETRY_DELAY = 1000
const MAX_RETRY_DELAY = 10000

interface RoomListener {
    onMessage: (data: any) => void
    onStatus?: (status: MonitorSocketStatus) => void
    onResync?: () => void
}

/**
 * SharedMultiplexedTransport：全浏览器窗口按 (wsUrl) 共享唯一一条物理 WebSocket 连接，
 * 多个组件（浮标 widget、当前页面 dashboard / env_health / roi / graph / artifacts、终端弹窗）
 * 通过 {"type":"group","action":"join"|"leave","room":...} 在同一条 WS 连接上多路复用房间订阅。
 * 既 100% 脱离浏览器 HTTP/1.1 同源 6 连接上限，也避免多组件重复握手。
 */
class SharedMultiplexedTransport {
    private url: string
    private ws: WebSocket | null = null
    private timer: ReturnType<typeof setTimeout> | null = null
    private retry = 0
    private everOpened = false
    private status: MonitorSocketStatus = 'connecting'
    private rooms = new Map<string, Set<RoomListener>>()
    private lastPayloads = new Map<string, Map<string, any>>()

    constructor(url: string) {
        this.url = url
    }

    get currentStatus(): MonitorSocketStatus {
        return this.status
    }

    subscribe(room: string, listener: RoomListener): () => void {
        let set = this.rooms.get(room)
        const isFirstInRoom = !set || set.size === 0
        if (!set) {
            set = new Set()
            this.rooms.set(room, set)
        }
        set.add(listener)
        listener.onStatus?.(this.status)

        const cachedEvents = this.lastPayloads.get(room)
        if (cachedEvents && cachedEvents.size > 0) {
            for (const payload of cachedEvents.values()) {
                queueMicrotask(() => {
                    if (this.rooms.get(room)?.has(listener)) {
                        listener.onMessage(payload)
                    }
                })
            }
        }

        if ((!this.ws || this.ws.readyState >= WebSocket.CLOSING) && !this.timer) {
            this.open()
        } else if (this.ws && this.ws.readyState === WebSocket.OPEN && isFirstInRoom) {
            try {
                this.ws.send(JSON.stringify({ type: 'group', action: 'join', room }))
            } catch {
                // ignore transient send error
            }
        }

        return () => {
            const cur = this.rooms.get(room)
            if (!cur) return
            cur.delete(listener)
            if (cur.size === 0) {
                this.rooms.delete(room)
                this.lastPayloads.delete(room)
                if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                    try {
                        this.ws.send(JSON.stringify({ type: 'group', action: 'leave', room }))
                    } catch {
                        // ignore
                    }
                }
            }
            if (this.rooms.size === 0) {
                this.closeAll()
            }
        }
    }

    closeAll(): void {
        if (this.timer) {
            clearTimeout(this.timer)
            this.timer = null
        }
        this.retry = 0
        this.status = 'connecting'
        const ws = this.ws
        this.ws = null
        if (ws) {
            ws.onopen = null
            ws.onmessage = null
            ws.onerror = null
            ws.onclose = null
            try {
                ws.close()
            } catch {
                // ignore
            }
        }
    }

    private broadcastStatus(st: MonitorSocketStatus) {
        this.status = st
        for (const set of this.rooms.values()) {
            for (const l of set) {
                l.onStatus?.(st)
            }
        }
    }

    private open() {
        if (this.rooms.size === 0) return
        this.broadcastStatus(this.everOpened ? 'reconnecting' : 'connecting')
        let ws: WebSocket
        try {
            ws = new WebSocket(this.url)
        } catch {
            this.scheduleReconnect()
            return
        }
        this.ws = ws

        ws.onopen = () => {
            if (this.ws !== ws) {
                ws.close()
                return
            }
            this.retry = 0
            this.broadcastStatus('connected')
            for (const room of this.rooms.keys()) {
                try {
                    ws.send(JSON.stringify({ type: 'group', action: 'join', room }))
                } catch {
                    // ignore
                }
            }
            if (this.everOpened) {
                for (const set of this.rooms.values()) {
                    for (const l of set) {
                        l.onResync?.()
                    }
                }
            }
            this.everOpened = true
        }

        ws.onmessage = (event) => {
            if (this.ws !== ws || typeof event.data !== 'string') return
            try {
                const msg = JSON.parse(event.data)
                if (msg?.type === 'group' && msg?.action === 'message' && msg.room && msg.data != null) {
                    const roomName = String(msg.room)
                    const evName = String(msg.data?.event || '')
                    if (evName && !evName.endsWith('_progress')) {
                        let evMap = this.lastPayloads.get(roomName)
                        if (!evMap) {
                            evMap = new Map()
                            this.lastPayloads.set(roomName, evMap)
                        }
                        evMap.set(evName, msg.data)
                    }
                    const set = this.rooms.get(roomName)
                    if (set) {
                        for (const l of set) {
                            l.onMessage(msg.data)
                        }
                    }
                }
            } catch {
                // ignore malformed frame
            }
        }

        ws.onerror = () => {
            try {
                ws.close()
            } catch {
                // ignore
            }
        }

        ws.onclose = () => {
            if (this.ws !== ws) return
            this.ws = null
            if (this.rooms.size === 0) return
            this.broadcastStatus('reconnecting')
            this.scheduleReconnect()
        }
    }

    private scheduleReconnect() {
        if (this.rooms.size === 0 || this.timer) return
        const delay = Math.min(MIN_RETRY_DELAY * 2 ** this.retry, MAX_RETRY_DELAY)
        this.retry++
        this.timer = setTimeout(() => {
            this.timer = null
            this.open()
        }, delay)
    }
}

const transports = new Map<string, SharedMultiplexedTransport>()

function buildWSURL(path: string, token: string): string | null {
    try {
        const base = getBaseURL() || window.location.origin
        const url = new URL(path || DEFAULT_PATH, base.startsWith('http') ? base : window.location.origin)
        url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
        url.searchParams.set('token', token)
        return url.toString()
    } catch {
        return null
    }
}

if (typeof window !== 'undefined') {
    const cleanAll = () => {
        for (const t of transports.values()) {
            t.closeAll()
        }
        transports.clear()
    }
    window.addEventListener('beforeunload', cleanAll)
    window.addEventListener('pagehide', cleanAll)
}

/**
 * 监控与控制台实时通道：
 * - 复用 /ws?token=，连上后通过 {"type":"group","action":"join","room":...} 订阅房间广播
 * - 同窗口多个 MonitorSocket 实例自动共享同一条底层 WebSocket 连接
 */
export class MonitorSocket {
    private opts: MonitorSocketOptions
    private unsub: (() => void) | null = null
    private current: MonitorSocketStatus = 'connecting'

    constructor(options: MonitorSocketOptions) {
        this.opts = options
    }

    get status(): MonitorSocketStatus {
        return this.current
    }

    start(): void {
        if (this.unsub) return
        if (!this.opts.token || !this.opts.room) {
            this.current = 'disabled'
            this.opts.onStatus?.('disabled')
            return
        }
        const wsUrl = buildWSURL(this.opts.path || DEFAULT_PATH, this.opts.token)
        if (!wsUrl) {
            this.current = 'disabled'
            this.opts.onStatus?.('disabled')
            return
        }
        let transport = transports.get(wsUrl)
        if (!transport) {
            transport = new SharedMultiplexedTransport(wsUrl)
            transports.set(wsUrl, transport)
        }
        this.unsub = transport.subscribe(this.opts.room, {
            onMessage: (payload) => this.opts.onMessage(payload),
            onStatus: (st) => {
                this.current = st
                this.opts.onStatus?.(st)
            },
            onResync: () => this.opts.onResync?.(),
        })
    }

    stop(): void {
        if (this.unsub) {
            const fn = this.unsub
            this.unsub = null
            fn()
        }
    }
}
