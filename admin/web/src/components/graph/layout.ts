/**
 * 图谱布局的**纯计算层**（不碰 DOM、不碰 Vue）——同一份代码既跑在主线程（降级路径），
 * 也跑在 Web Worker（正常路径，见 `layout.worker.ts`）。
 *
 * 双模态布局架构：
 *   ① 全景星系模式（`computeLayout`）：
 *     - 黄金螺旋枢纽定锚 + 度数为 1 的叶子节点围绕父枢纽环形落位；
 *     - 拓扑感知弹簧引力 + 多体斥力 + 碰撞检测（forceCollide）+ 增量坐标复用（prevCoords）；
 *   ② 单节点独立下钻模式（`computeEgoLayout`）：
 *     - 焦点根节点（focusId）严格锚定在中心 `(0, 0)`；
 *     - BFS 计算每个节点距焦点节点的精确跳数（Hop 1 / Hop 2 / Hop 3）；
 *     - 1 跳邻居按关系谓词与扇区均匀分布在第一同心环（R1）上，2 跳邻居围绕其所属 1 跳父节点扇形分布在第二同心环（R2）上；
 *     - 辅以轻量切向防重叠碰撞松弛（保持径向圆环距离恒定，绝不散乱重叠）。
 */

export interface LayoutNode {
    id: number
    degree: number
}

export interface LayoutEdge {
    from: number
    to: number
    relation?: string
}

export interface LayoutResult {
    coords: Record<number, [number, number]>
    reuse: number
    ticks: number
    hopMap?: Record<number, number>
    ringRadii?: number[]
}

interface SimNode {
    id: number
    degree: number
    x: number
    y: number
    vx: number
    vy: number
    radius: number
}

/** 节点间距常数：图直径 ≈ SPACING · √n，与容器尺寸无关 */
export const LAYOUT_SPACING = 62

/** 节点大小：度数开方缩放（1 度 12px、4 度 16px、9 度 20px、25 度 30px 封顶） */
export function sizeOf(degree: number): number {
    return Math.min(30, 7 + Math.sqrt(Math.max(0, degree)) * 4.6)
}

/**
 * 单节点独立下钻布局（Ego-Centric Radial Concentric Layout）：
 * - focusId 固定在 (0, 0)，坐标等向同性（完美同心圆环，便于叠加轨道引导圈）
 * - 1-Hop 直接邻居排列在 R1 同心圆环上（按关联谓词聚合排序，使相同关系的邻居归拢在同一扇区）
 * - 2-Hop 间接邻居围绕其 1-Hop 桥接父节点排列在 R2 外环扇区上
 * - 3-Hop+ 或未连通节点排列在 R3 外环
 */
export function computeEgoLayout(nodes: LayoutNode[], edges: LayoutEdge[], focusId: number): LayoutResult {
    const n = nodes.length
    if (n === 0) {
        return { coords: {}, reuse: 1, ticks: 0, hopMap: {}, ringRadii: [] }
    }

    const idSet = new Set(nodes.map((x) => x.id))
    if (!idSet.has(focusId)) {
        return computeLayout(nodes, edges, 1.2, {})
    }

    // 构建无向邻接表 + 记录主关系词与方向用于同心环扇区聚类排序
    const adj = new Map<number, { peer: number; rel: string; dir: number }[]>()
    for (const item of nodes) {
        adj.set(item.id, [])
    }
    for (const e of edges) {
        if (!idSet.has(e.from) || !idSet.has(e.to) || e.from === e.to) continue
        const rel = e.relation || ''
        adj.get(e.from)!.push({ peer: e.to, rel, dir: 1 })
        adj.get(e.to)!.push({ peer: e.from, rel, dir: -1 })
    }

    // BFS 计算距焦点节点的跳数（hop）与前驱桥接节点（parentOf）
    const hopMap: Record<number, number> = { [focusId]: 0 }
    const parentOf = new Map<number, number>()
    const parentRel = new Map<number, string>()
    const queue: number[] = [focusId]

    while (queue.length > 0) {
        const cur = queue.shift()!
        const curHop = hopMap[cur]!
        const neighbors = adj.get(cur) || []
        for (const nb of neighbors) {
            if (hopMap[nb.peer] === undefined) {
                hopMap[nb.peer] = curHop + 1
                parentOf.set(nb.peer, cur)
                parentRel.set(nb.peer, `${nb.dir > 0 ? 'OUT' : 'IN'}:${nb.rel}`)
                queue.push(nb.peer)
            }
        }
    }

    const hop1: LayoutNode[] = []
    const hop2: LayoutNode[] = []
    const hop3: LayoutNode[] = []

    for (const item of nodes) {
        if (item.id === focusId) continue
        const h = hopMap[item.id] ?? 3
        hopMap[item.id] = h
        if (h === 1) hop1.push(item)
        else if (h === 2) hop2.push(item)
        else hop3.push(item)
    }

    // 1-Hop 按 (方向+谓词, 度数降序, id) 排序，让同谓词的直接邻居整齐聚拢在相邻角度
    hop1.sort((a, b) => {
        const ra = parentRel.get(a.id) || ''
        const rb = parentRel.get(b.id) || ''
        if (ra !== rb) return ra.localeCompare(rb)
        if (b.degree !== a.degree) return b.degree - a.degree
        return a.id - b.id
    })

    // 自适应计算同心环半径：保证环上节点周长间距充裕，标签不打架；
    // 半径上限提高让内环（1 跳）相对外环更大，配合 fit 缩放可显著拉开环上节点间距
    const r1 = Math.max(150, Math.min(360, (hop1.length * 40) / (Math.PI * 2) + 105))
    const r2 = Math.max(r1 + 150, Math.min(620, (hop2.length * 32) / (Math.PI * 2) + r1 + 100))
    const r3 = r2 + 130

    // 环带分层：单环上节点过密时按数量自适应拆成 2~3 条并列子环带，把环上间距成倍拉开
    function bandsFor(count: number, base: number, gap: number): number[] {
        if (count <= 0) return []
        if (count <= 40) return [base]
        if (count <= 90) return [base - gap / 2, base + gap / 2]
        return [base - gap, base, base + gap]
    }

    const coords: Record<number, [number, number]> = {
        [focusId]: [0, 0],
    }
    const angleOf = new Map<number, number>()

    // 排布 1-Hop 同心环带（同谓词的直接邻居按排序各占连续扇区，大扇出自动分多条子环带）
    const bands1 = bandsFor(hop1.length, r1, 66)
    const perBand1 = bands1.length ? Math.ceil(hop1.length / bands1.length) : hop1.length
    hop1.forEach((item, idx) => {
        const band = Math.min(bands1.length - 1, Math.floor(idx / Math.max(1, perBand1)))
        const j = idx - band * perBand1
        const count = Math.min(perBand1, hop1.length - band * perBand1)
        const theta = -Math.PI / 2 + ((j + 0.5 * band) / Math.max(1, count)) * Math.PI * 2
        angleOf.set(item.id, theta)
        const rad = bands1[band] ?? r1
        coords[item.id] = [Math.round(Math.cos(theta) * rad * 10) / 10, Math.round(Math.sin(theta) * rad * 10) / 10]
    })

    // 排布 2-Hop 同心环：按其所属 1-Hop 桥接父节点的角度对齐扇区
    hop2.sort((a, b) => {
        const pa = parentOf.get(a.id) ?? 0
        const pb = parentOf.get(b.id) ?? 0
        const aa = angleOf.get(pa) ?? 0
        const ab = angleOf.get(pb) ?? 0
        if (Math.abs(aa - ab) > 1e-4) return aa - ab
        const ra = parentRel.get(a.id) || ''
        const rb = parentRel.get(b.id) || ''
        if (ra !== rb) return ra.localeCompare(rb)
        return b.degree - a.degree
    })

    // 统计每个 1-Hop 父节点挂载的 2-Hop 子节点以扇形展开，若过多则均匀融合全局角度
    const byParent = new Map<number, LayoutNode[]>()
    for (const item of hop2) {
        const p = parentOf.get(item.id) ?? 0
        if (!byParent.has(p)) byParent.set(p, [])
        byParent.get(p)!.push(item)
    }

    const placedAngles: { id: number; theta: number }[] = []
    for (const [pId, children] of byParent.entries()) {
        const baseTheta = angleOf.get(pId) ?? 0
        const spread = Math.min(Math.PI * 0.45, Math.max(0.18, children.length * 0.11))
        children.forEach((ch, idx) => {
            const offset = children.length === 1 ? 0 : (idx / (children.length - 1) - 0.5) * spread
            placedAngles.push({ id: ch.id, theta: baseTheta + offset })
        })
    }

    // 对 2-Hop 角度做一次最小角间距均匀松弛，防止相邻大簇扇区挤压
    placedAngles.sort((a, b) => a.theta - b.theta)
    const minStep = Math.min(0.16, (Math.PI * 2) / Math.max(8, placedAngles.length * 1.15))
    for (let pass = 0; pass < 6; pass++) {
        for (let i = 0; i < placedAngles.length; i++) {
            const cur = placedAngles[i]!
            const nxt = placedAngles[(i + 1) % placedAngles.length]!
            let diff = nxt.theta - cur.theta
            if (i === placedAngles.length - 1) diff += Math.PI * 2
            if (diff < minStep) {
                const push = (minStep - diff) * 0.45
                cur.theta -= push
                nxt.theta += push
            }
        }
    }

    // 排布 2-Hop 同心环带：角度松弛后按序交错落入多条子环带，进一步拉开环上间距
    const bands2 = bandsFor(hop2.length, r2, 58)
    placedAngles.forEach((entry, idx) => {
        angleOf.set(entry.id, entry.theta)
        const band = bands2.length ? idx % bands2.length : 0
        const rad = bands2[band] ?? r2
        coords[entry.id] = [Math.round(Math.cos(entry.theta) * rad * 10) / 10, Math.round(Math.sin(entry.theta) * rad * 10) / 10]
    })

    // 排布 3-Hop+ 外圈节点
    hop3.forEach((item, idx) => {
        const pId = parentOf.get(item.id) ?? 0
        const base = angleOf.get(pId) ?? -Math.PI / 2 + (idx / Math.max(1, hop3.length)) * Math.PI * 2
        const theta = base + ((idx % 5) - 2) * 0.06
        coords[item.id] = [Math.round(Math.cos(theta) * r3 * 10) / 10, Math.round(Math.sin(theta) * r3 * 10) / 10]
    })

    const ringRadii: number[] = []
    for (const b of bands1) {
        ringRadii.push(Math.round(b))
    }
    for (const b of bands2) {
        ringRadii.push(Math.round(b))
    }
    if (hop3.length > 0) {
        ringRadii.push(Math.round(r3))
    }

    return {
        coords,
        reuse: 1,
        ticks: 6,
        hopMap,
        ringRadii,
    }
}

/**
 * 确定性力导向 + 碰撞检测布局（同步、纯函数，主线程与 Worker 同源）
 */
export function computeLayout(nodes: LayoutNode[], edges: LayoutEdge[], aspect: number, prev: Record<number, [number, number]>): LayoutResult {
    const n = nodes.length
    if (n === 0) {
        return { coords: {}, reuse: 0, ticks: 0 }
    }

    const clampedAspect = Math.min(2, Math.max(0.5, aspect || 1))
    const goldenAngle = Math.PI * (3 - Math.sqrt(5))
    let reuse = 0

    const idToIdx = new Map<number, number>()
    nodes.forEach((item, idx) => idToIdx.set(item.id, idx))

    // 统计画布内实际连接度数与最高度数邻居（用于将叶子节点直接初始化在其所属枢纽周围）
    const adj = Array.from({ length: n }, () => [] as number[])
    for (const e of edges) {
        const si = idToIdx.get(e.from)
        const ti = idToIdx.get(e.to)
        if (si !== undefined && ti !== undefined && si !== ti) {
            adj[si]!.push(ti)
            adj[ti]!.push(si)
        }
    }

    const degCount = adj.map((list) => list.length)

    // 先按度数降序排列枢纽索引，使高连接度中心均匀分布在画布黄金螺旋骨架上
    const order = Array.from({ length: n }, (_, i) => i).sort((a, b) => (degCount[b] || 0) - (degCount[a] || 0))
    const seedX = new Array<number>(n).fill(0)
    const seedY = new Array<number>(n).fill(0)
    const seeded = new Array<boolean>(n).fill(false)

    let hubRank = 0
    const hubSpacing = n > 600 ? 78 : 28
    for (const idx of order) {
        if ((degCount[idx] || 0) <= 1 && hubRank > 0) continue
        const r = hubSpacing * Math.sqrt(hubRank + 1)
        const theta = hubRank * goldenAngle
        seedX[idx] = r * Math.cos(theta) * Math.sqrt(clampedAspect)
        seedY[idx] = (r * Math.sin(theta)) / Math.sqrt(clampedAspect)
        seeded[idx] = true
        hubRank++
    }

    // 度数为 1 的叶子节点直接围绕其父枢纽节点按子环均匀落位，消除跨屏飞线与散沙感
    const parentLeafIdx = new Map<number, number>()
    for (let i = 0; i < n; i++) {
        if (seeded[i]) continue
        const neighbors = adj[i]!
        if (neighbors.length > 0) {
            let bestParent = neighbors[0]!
            for (const nb of neighbors) {
                if ((degCount[nb] || 0) > (degCount[bestParent] || 0)) {
                    bestParent = nb
                }
            }
            if (!seeded[bestParent]) {
                const r = hubSpacing * Math.sqrt(hubRank + 1)
                const theta = hubRank * goldenAngle
                seedX[bestParent] = r * Math.cos(theta) * Math.sqrt(clampedAspect)
                seedY[bestParent] = (r * Math.sin(theta)) / Math.sqrt(clampedAspect)
                seeded[bestParent] = true
                hubRank++
            }
            const k = parentLeafIdx.get(bestParent) ?? 0
            parentLeafIdx.set(bestParent, k + 1)
            const ringIdx = Math.floor(k / 10)
            const slotInRing = k % 10
            const angle = (slotInRing / 10) * Math.PI * 2 + ringIdx * 0.31
            const leafDist = 30 + ringIdx * 20
            seedX[i] = seedX[bestParent]! + Math.cos(angle) * leafDist
            seedY[i] = seedY[bestParent]! + Math.sin(angle) * leafDist
            seeded[i] = true
        } else {
            const r = (hubSpacing + 2) * Math.sqrt(hubRank + 1)
            const theta = hubRank * goldenAngle
            seedX[i] = r * Math.cos(theta) * Math.sqrt(clampedAspect)
            seedY[i] = (r * Math.sin(theta)) / Math.sqrt(clampedAspect)
            seeded[i] = true
            hubRank++
        }
    }

    // 大规模星系图（n > 600，如 1024 / 2048 / 4096 / 8192 / ALL）：
    // 直接采用 O(N) 黄金螺旋星系团 + 局部同心子环确定性坐标，免除 O(N^2) 两两斥力迭代，6ms 内极速返回！
    if (n > 600) {
        const fastOut: Record<number, [number, number]> = {}
        let cx = 0
        let cy = 0
        for (let i = 0; i < n; i++) {
            cx += seedX[i]!
            cy += seedY[i]!
        }
        cx /= n
        cy /= n
        for (let i = 0; i < n; i++) {
            const item = nodes[i]!
            const p = prev[item.id]
            if (p && Number.isFinite(p[0]) && Number.isFinite(p[1])) {
                reuse++
                fastOut[item.id] = p
            } else {
                fastOut[item.id] = [Math.round((seedX[i]! - cx) * 10) / 10, Math.round((seedY[i]! - cy) * 10) / 10]
            }
        }
        return { coords: fastOut, reuse: reuse / n, ticks: 1 }
    }

    const simNodes: SimNode[] = nodes.map((item, idx) => {
        const p = prev[item.id]
        const deg = degCount[idx] || item.degree || 1
        const rad = sizeOf(deg) / 2 + 5
        if (p && Number.isFinite(p[0]) && Number.isFinite(p[1])) {
            reuse++
            return {
                id: item.id,
                degree: deg,
                x: p[0],
                y: p[1],
                vx: 0,
                vy: 0,
                radius: rad,
            }
        }
        return {
            id: item.id,
            degree: deg,
            x: seedX[idx]!,
            y: seedY[idx]!,
            vx: 0,
            vy: 0,
            radius: rad,
        }
    })

    const nodeIndex = idToIdx

    const links: { s: number; t: number; bias: number; strength: number; distance: number }[] = []
    for (const e of edges) {
        const si = nodeIndex.get(e.from)
        const ti = nodeIndex.get(e.to)
        if (si !== undefined && ti !== undefined && si !== ti) {
            const ds = degCount[si] || 1
            const dt = degCount[ti] || 1
            const minDeg = Math.min(ds, dt)
            const maxDeg = Math.max(ds, dt)
            const isLeaf = minDeg <= 1
            // 叶子节点紧贴所属主语/枢纽（短距强拉），骨干枢纽间拉开合适间距
            const dist = isLeaf ? 30 + Math.min(18, Math.sqrt(maxDeg) * 3.5) : LAYOUT_SPACING + Math.min(36, Math.sqrt(ds + dt) * 6)
            const strength = isLeaf ? 0.68 : 0.45 / Math.sqrt(minDeg)
            links.push({
                s: si,
                t: ti,
                bias: ds / (ds + dt),
                strength,
                distance: dist,
            })
        }
    }

    const reuseRatio = n > 0 ? reuse / n : 0
    const ticks = reuseRatio > 0.6 ? 80 : n <= 220 ? 280 : n <= 600 ? 190 : 110
    const alphaMin = 0.001
    const alphaDecay = 1 - Math.pow(alphaMin, 1 / ticks)
    const velocityDecay = 0.58
    const chargeStrength = -LAYOUT_SPACING * 1.15
    const xStrength = 0.032
    const yStrength = 0.032 * clampedAspect

    let alpha = reuseRatio > 0.6 ? 0.35 : 1.0

    for (let step = 0; step < ticks; step++) {
        alpha += (0 - alpha) * alphaDecay

        // 1. 拓扑感知弹簧引力（forceLink）
        for (const lk of links) {
            const source = simNodes[lk.s]!
            const target = simNodes[lk.t]!
            let dx = target.x + target.vx - source.x - source.vx
            let dy = target.y + target.vy - source.y - source.vy
            let l = Math.sqrt(dx * dx + dy * dy)
            if (l < 1e-6) l = 1e-6
            const delta = ((l - lk.distance) / l) * alpha * lk.strength
            dx *= delta
            dy *= delta
            target.vx -= dx * lk.bias
            target.vy -= dy * lk.bias
            source.vx += dx * (1 - lk.bias)
            source.vy += dy * (1 - lk.bias)
        }

        // 2. 多体斥力（forceManyBody，枢纽节点斥力按度数温和放大以撑开星系簇）
        for (let i = 0; i < n; i++) {
            const ni = simNodes[i]!
            const wi = 1 + Math.min(1.8, Math.sqrt(ni.degree) * 0.22)
            for (let j = i + 1; j < n; j++) {
                const nj = simNodes[j]!
                let dx = nj.x - ni.x
                let dy = nj.y - ni.y
                let distSq = dx * dx + dy * dy
                if (distSq < 1) {
                    dx = (i % 2 === 0 ? 1 : -1) * 0.5
                    dy = (j % 2 === 0 ? 1 : -1) * 0.5
                    distSq = 1
                }
                if (distSq > 260000) continue
                const wj = 1 + Math.min(1.8, Math.sqrt(nj.degree) * 0.22)
                const w = (chargeStrength * wi * wj * alpha) / distSq
                ni.vx += dx * w
                ni.vy += dy * w
                nj.vx -= dx * w
                nj.vy -= dy * w
            }
        }

        // 3. 各向异性向心引力（forceX / forceY）
        for (let i = 0; i < n; i++) {
            const d = simNodes[i]!
            d.vx += (0 - d.x) * xStrength * alpha
            d.vy += (0 - d.y) * yStrength * alpha
        }

        // 4. 碰撞检测与排斥（forceCollide）
        for (let i = 0; i < n; i++) {
            const ni = simNodes[i]!
            for (let j = i + 1; j < n; j++) {
                const nj = simNodes[j]!
                const minDist = ni.radius + nj.radius
                let dx = nj.x + nj.vx - (ni.x + ni.vx)
                let dy = nj.y + nj.vy - (ni.y + ni.vy)
                const distSq = dx * dx + dy * dy
                if (distSq < minDist * minDist) {
                    const dist = Math.sqrt(distSq) || 0.01
                    const overlap = ((minDist - dist) / dist) * 0.45
                    dx *= overlap
                    dy *= overlap
                    nj.vx += dx
                    nj.vy += dy
                    ni.vx -= dx
                    ni.vy -= dy
                }
            }
        }

        // 5. 积分与质心居中（forceCenter）
        let cx = 0
        let cy = 0
        for (let i = 0; i < n; i++) {
            const d = simNodes[i]!
            d.vx *= velocityDecay
            d.vy *= velocityDecay
            d.x += d.vx
            d.y += d.vy
            cx += d.x
            cy += d.y
        }
        cx /= n
        cy /= n
        for (let i = 0; i < n; i++) {
            const d = simNodes[i]!
            d.x -= cx
            d.y -= cy
        }
    }

    const out: Record<number, [number, number]> = {}
    for (const d of simNodes) {
        out[d.id] = [Math.round(d.x * 10) / 10, Math.round(d.y * 10) / 10]
    }

    return { coords: out, reuse: reuseRatio, ticks }
}
