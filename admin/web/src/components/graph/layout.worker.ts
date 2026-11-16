import { computeLayout, type LayoutEdge, type LayoutNode } from './layout'

interface LayoutRequest {
    seq: number
    nodes: LayoutNode[]
    edges: LayoutEdge[]
    aspect: number
    prev: Record<number, [number, number]>
}

self.onmessage = (e: MessageEvent<LayoutRequest>) => {
    const { seq, nodes, edges, aspect, prev } = e.data
    const res = computeLayout(nodes, edges, aspect, prev)
    ;(self as unknown as Worker).postMessage({ seq, ...res })
}
