/**
 * Cosmic Nebula × Memory Field 3D Hypergraph Engine（**单源正本**：首页 `views/home/MemoryField.vue`、
 * 后台 `views/admin/dashboard/graph.vue` 与插件视图路径共同引用本文件，仓库内不再保留任何副本）
 *
 * 视觉美学与第一性原理：
 *   1. 拒绝任何廉价的机械多面体（八面体/方块/Emoji 贴图），采用自定义 GLSL 菲涅尔光晕球壳着色器（Fresnel Soma Membrane Shader）
 *      与高斯径向光晕粒子（Additive Gaussian Glow Sprites），呈现「宇宙星云 × 活体记忆容器 × 突触动作电位脉冲传导」的科研级生命感：
 *      - `Cell`（记忆地址容器 / 星云母体）：中心发光核心（Soma Core）+ 半透明菲涅尔呼吸光晕球壳（Fresnel Membrane Sphere，
 *        随时间正弦呼吸律动，内部通透包裹着所属的记忆微粒）；
 *      - `Unit`（容器内记忆囊泡 / 突触微粒）：按黄金螺旋斐波那契球面内轨（Fibonacci Sphere Intracellular Orbit）悬浮在所属 Cell
 *        光晕球壳内部，或作为高维知性神经节（L3 Noetic Ganglion）悬浮于星云上方；
 *      - `Edges`（神经轴突与跨地址突触）：采用三维有机平滑曲线（QuadraticBezierCurve3），并在曲线上实时驱动发光的
 *        动作电位电脉冲粒子（Action Potential Photons）沿轴突连续传导流动。
 *   2. 交互完备性：
 *      - 阻尼轨道相机（Damped OrbitControls）+ 三次缓动平滑相机飞行（Cubic Ease-Out Camera Fly-To）；
 *      - GPU Raycaster 精确拾取（悬停高亮当前容器/神经元与相连轴突脉冲、触发悬浮 Tooltip，单击定位并打开详情，双击进入 3D 同心球壳下钻视图）。
 */

import * as THREE from 'three'
import { OrbitControls } from 'three-stdlib'

export interface Graph3DNode {
    id: number
    node_key: string
    summary: string
    kind: string
    layer?: string
    weight?: number
    status: string
    confidence: number
    observed_at: string
    anchor: string
    span: string
    degree: number
    viewDegree: number
    atoms?: { token: string; potential: number; weight: number }[]
    molecules?: { text: string; mass_nats: number; gamma: number }[]
}

export interface Graph3DEdge {
    id: number
    from: number
    to: number
    from_key: string
    to_key: string
    from_label: string
    to_label: string
    relation: string
    layer?: string
    weight?: number
    anchor: string
    span: string
}

export type Graph3DSpaceMode = 'sphere' | 'disc'

export interface Graph3DRenderInput {
    nodes: Graph3DNode[]
    edges: Graph3DEdge[]
    coords2D: Record<number, [number, number]>
    inDrill: boolean
    focusId: number | null
    hopMap: Record<number, number>
    selectedNodeId: number | null
    showEdgeLabels: boolean
    isDark: boolean
    spaceMode?: Graph3DSpaceMode
}

export interface Graph3DCallbacks {
    onNodeClick: (node: Graph3DNode) => void
    onNodeDblClick: (node: Graph3DNode) => void
    onNodeHover: (node: Graph3DNode | null, clientX: number, clientY: number) => void
    enableZoom?: boolean
}

// ==================== GLSL 电影级 3D FBM 分形湍流星云包络着色器（Volumetric FBM Nebula Soma Shader） ====================

const SOMA_MEMBRANE_VERTEX_SHADER = /* glsl */ `
    varying vec3 vNormal;
    varying vec3 vViewPosition;
    varying vec3 vLocalPos;
    uniform float uTime;
    uniform float uPulsePhase;

    void main() {
        vLocalPos = position;
        // 有机星云团的多频微幅谐波呼吸形变（打破死板的正圆球几何感）
        float wave1 = sin(position.x * 3.2 + uTime * 1.4 + uPulsePhase) * cos(position.y * 2.8 - uTime * 1.1);
        float wave2 = sin(position.z * 3.6 - uTime * 1.6 + uPulsePhase * 1.7);
        float breathe = 1.0 + 0.035 * sin(uTime * 1.6 + uPulsePhase) + 0.042 * (wave1 + wave2 * 0.5);
        vec3 pos = position * breathe;
        vec4 mvPosition = modelViewMatrix * vec4(pos, 1.0);
        vNormal = normalize(normalMatrix * normal);
        vViewPosition = -mvPosition.xyz;
        gl_Position = projectionMatrix * mvPosition;
    }
`

const SOMA_MEMBRANE_FRAGMENT_SHADER = /* glsl */ `
    varying vec3 vNormal;
    varying vec3 vViewPosition;
    varying vec3 vLocalPos;
    uniform float uTime;
    uniform float uPulsePhase;
    uniform vec3 uColor;
    uniform vec3 uRimColor;
    uniform float uOpacity;
    uniform float uHighlight;

    // 3D 伪随机哈希与三线性插值价值噪声（Value Noise 3D）
    float hash31(vec3 p) {
        p = fract(p * vec3(443.897, 441.423, 437.195));
        p += dot(p, p.yzx + 19.19);
        return fract((p.x + p.y) * p.z);
    }

    float noise3D(vec3 p) {
        vec3 i = floor(p);
        vec3 f = fract(p);
        f = f * f * (3.0 - 2.0 * f);
        return mix(
            mix(
                mix(hash31(i + vec3(0,0,0)), hash31(i + vec3(1,0,0)), f.x),
                mix(hash31(i + vec3(0,1,0)), hash31(i + vec3(1,1,0)), f.x),
                f.y
            ),
            mix(
                mix(hash31(i + vec3(0,0,1)), hash31(i + vec3(1,0,1)), f.x),
                mix(hash31(i + vec3(0,1,1)), hash31(i + vec3(1,1,1)), f.x),
                f.y
            ),
            f.z
        );
    }

    // 4 层八度分形布朗运动（Fractal Brownian Motion），合成丝绸状星际湍流云雾
    float fbm3D(vec3 p) {
        float v = 0.0;
        float amp = 0.52;
        for (int i = 0; i < 4; i++) {
            v += amp * noise3D(p);
            p = p * 2.07 + vec3(1.7, 9.2, 5.3);
            amp *= 0.5;
        }
        return v;
    }

    void main() {
        vec3 normal = normalize(vNormal);
        vec3 viewDir = normalize(vViewPosition);
        float ndotv = abs(dot(normal, viewDir));

        // 旋涡场扭曲采样（Domain Warping）：让星云内部呈现真实流动的电离气体灯丝与暗星云空腔
        vec3 flowCoord = vLocalPos * 2.4 + vec3(uTime * 0.16, -uTime * 0.12, uPulsePhase);
        float q = fbm3D(flowCoord);
        float plasma = fbm3D(flowCoord + vec3(q * 1.85) + vec3(0.0, uTime * 0.14, 0.0));
        float filament = smoothstep(0.32, 0.78, plasma);

        // 柔和星云边缘衰减 + 内部丝状等离子高亮（避免硬边肥皂泡感）
        float rimGlow = pow(1.0 - clamp(ndotv, 0.0, 1.0), 1.75) * smoothstep(0.0, 0.22, ndotv);
        float nebulaDensity = (0.04 + 0.26 * filament) * (0.45 + 0.55 * (1.0 - ndotv));
        float alpha = clamp(nebulaDensity + rimGlow * (uOpacity + 0.34 * uHighlight) * (0.6 + 0.7 * filament), 0.0, 0.85);

        vec3 hotFilament = mix(uColor, uRimColor, clamp(filament * 1.15 + rimGlow * 0.5, 0.0, 1.0));
        vec3 col = mix(hotFilament, vec3(0.92, 0.98, 1.0), pow(filament, 3.2) * (0.28 + 0.35 * uHighlight));
        gl_FragColor = vec4(col, alpha);
    }
`

// ==================== GLSL 电影级十字星芒闪烁粒子着色器（Anamorphic Twinkling Starfield Shader） ====================

const STARFIELD_VERTEX_SHADER = /* glsl */ `
    attribute float aSize;
    attribute float aPhase;
    varying vec3 vColor;
    varying float vTwinkle;
    uniform float uTime;

    void main() {
        vColor = color;
        vTwinkle = 0.45 + 0.55 * sin(uTime * 2.4 + aPhase);
        vec4 mvPosition = modelViewMatrix * vec4(position, 1.0);
        gl_PointSize = clamp(aSize * (520.0 / max(120.0, -mvPosition.z)) * (0.75 + 0.45 * vTwinkle), 1.5, 22.0);
        gl_Position = projectionMatrix * mvPosition;
    }
`

const STARFIELD_FRAGMENT_SHADER = /* glsl */ `
    varying vec3 vColor;
    varying float vTwinkle;

    void main() {
        vec2 uv = gl_PointCoord - vec2(0.5);
        float d = length(uv) * 2.0;
        if (d > 1.0) discard;
        // 高斯圆核 + 电影镜头十字衍射星芒（Anamorphic Cross Flare）
        float core = exp(-d * d * 7.5);
        float crossX = exp(-abs(uv.y) * 26.0) * exp(-abs(uv.x) * 4.5);
        float crossY = exp(-abs(uv.x) * 26.0) * exp(-abs(uv.y) * 4.5);
        float flare = clamp(core + (crossX + crossY) * 0.55 * vTwinkle, 0.0, 1.0);
        vec3 col = mix(vColor, vec3(1.0), core * 0.65);
        gl_FragColor = vec4(col, flare * (0.35 + 0.55 * vTwinkle));
    }
`

// ==================== GLSL 彗星拖尾动作电位脉冲光子着色器（Comet-Tail Action Potential Shader） ====================

const PULSE_PHOTON_VERTEX_SHADER = /* glsl */ `
    attribute float aSize;
    attribute float aAlpha;
    varying vec3 vColor;
    varying float vAlpha;

    void main() {
        vColor = color;
        vAlpha = aAlpha;
        vec4 mvPosition = modelViewMatrix * vec4(position, 1.0);
        gl_PointSize = clamp(aSize * (560.0 / max(90.0, -mvPosition.z)), 2.5, 36.0);
        gl_Position = projectionMatrix * mvPosition;
    }
`

const PULSE_PHOTON_FRAGMENT_SHADER = /* glsl */ `
    varying vec3 vColor;
    varying float vAlpha;

    void main() {
        vec2 uv = gl_PointCoord - vec2(0.5);
        float d = length(uv) * 2.0;
        if (d > 1.0) discard;
        float glow = pow(1.0 - d, 2.2);
        float hotCore = exp(-d * d * 10.0);
        vec3 col = mix(vColor, vec3(1.0, 1.0, 1.0), hotCore * 0.78);
        gl_FragColor = vec4(col, glow * vAlpha);
    }
`

// ==================== 径向高斯星云光晕纹理生成器 ====================

function createRadialGlowTexture(innerHex: string, midHex: string, outerRgba = 'rgba(0,0,0,0)'): THREE.CanvasTexture {
    const size = 128
    const canvas = document.createElement('canvas')
    canvas.width = size
    canvas.height = size
    const ctx = canvas.getContext('2d')!
    const half = size / 2
    const grad = ctx.createRadialGradient(half, half, 0, half, half, half)
    // 色相守恒：核心直接使用层级本色（禁白色核心，否则加法混合会把三层颜色冲成同一片白）
    grad.addColorStop(0.0, innerHex)
    grad.addColorStop(0.2, innerHex)
    grad.addColorStop(0.55, midHex)
    grad.addColorStop(1.0, outerRgba)
    ctx.fillStyle = grad
    ctx.fillRect(0, 0, size, size)
    const tex = new THREE.CanvasTexture(canvas)
    tex.needsUpdate = true
    return tex
}

function createTextLabelSprite(text: string, accentColor: string, isCell: boolean, _isDark: boolean): THREE.Sprite {
    const canvas = document.createElement('canvas')
    const ctx = canvas.getContext('2d')!
    const dpr = 2
    const fontSize = isCell ? 12.5 * dpr : 11 * dpr
    const font = `${isCell ? 600 : 500} ${fontSize}px -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif`
    ctx.font = font
    const metrics = ctx.measureText(text)
    const padX = 10 * dpr
    const padY = 5 * dpr
    const w = Math.ceil(metrics.width + padX * 2 + 8 * dpr)
    const h = Math.ceil(fontSize + padY * 2)
    canvas.width = w
    canvas.height = h

    ctx.font = font
    // 科幻切角全息投影铭牌（Chamfered Holographic HUD Callout）
    const cut = 5 * dpr
    ctx.beginPath()
    ctx.moveTo(cut, 0)
    ctx.lineTo(w - cut, 0)
    ctx.lineTo(w, cut)
    ctx.lineTo(w, h - cut)
    ctx.lineTo(w - cut, h)
    ctx.lineTo(cut, h)
    ctx.lineTo(0, h - cut)
    ctx.lineTo(0, cut)
    ctx.closePath()

    ctx.fillStyle = 'rgba(3, 8, 18, 0.82)'
    ctx.fill()
    ctx.lineWidth = 1.1 * dpr
    ctx.strokeStyle = isCell ? accentColor : 'rgba(56, 189, 248, 0.32)'
    ctx.stroke()

    // 左侧全息光谱能量竖条
    ctx.fillStyle = accentColor
    ctx.fillRect(4 * dpr, 4 * dpr, 2.5 * dpr, h - 8 * dpr)

    // 底部发光准星刻度线
    ctx.fillStyle = accentColor
    ctx.fillRect(w * 0.35, h - 1.5 * dpr, w * 0.3, 1.5 * dpr)

    // 绘制微辉光文字
    ctx.shadowColor = accentColor
    ctx.shadowBlur = 4 * dpr
    ctx.fillStyle = '#f8fafc'
    ctx.textBaseline = 'middle'
    ctx.fillText(text, padX + 4 * dpr, h / 2 + 0.5 * dpr)
    ctx.shadowBlur = 0

    const tex = new THREE.CanvasTexture(canvas)
    tex.minFilter = THREE.LinearFilter
    tex.magFilter = THREE.LinearFilter
    const mat = new THREE.SpriteMaterial({
        map: tex,
        transparent: true,
        depthTest: false,
        depthWrite: false,
    })
    const sprite = new THREE.Sprite(mat)
    const scaleFactor = 0.21 / dpr
    sprite.scale.set(w * scaleFactor, h * scaleFactor, 1)
    sprite.renderOrder = 20
    return sprite
}

interface AxonPulseStream {
    curve: THREE.QuadraticBezierCurve3
    relation: string
    fromId: number
    toId: number
    baseSpeed: number
    phases: number[]
    color: THREE.Color
}

/** 节点跨刷新平滑位移与新节点生长过渡状态 */
interface NodeTransitionState {
    nodeId: number
    nodeKey: string
    startPos: THREE.Vector3
    targetPos: THREE.Vector3
    bornAt: number
    duration: number
    isNew: boolean
    spawnScale: number
    membraneMesh?: THREE.Mesh
    plumeSprite?: THREE.Sprite
    somaRad: number
    labelSprite?: THREE.Sprite
    labelZLift: number
}

/** 确定性伪随机序列生成器（消除背景星尘在数据刷新时的随机跳变） */
function deterministicUnitRandom(seed: number): number {
    const x = Math.sin(seed * 12.9898 + 78.233) * 43758.5453123
    return x - Math.floor(x)
}

/** 计算神经轴突三维二次贝塞尔曲线控制点 */
function computeAxonControlPoint(
    pA: THREE.Vector3,
    pB: THREE.Vector3,
    relation: string,
    isSphere: boolean,
    outMid = new THREE.Vector3()
): THREE.Vector3 {
    const isCites = relation === 'cites'
    const isWeak = relation === 'weak_edge' || relation === 'epistemic_void_snap'
    const isMember = relation === 'member_of'
    outMid.addVectors(pA, pB).multiplyScalar(0.5)
    const dist = pA.distanceTo(pB)
    if (isSphere && !isMember) {
        const radialDir = outMid.lengthSq() > 1 ? outMid.clone().normalize() : new THREE.Vector3(0, 0, 1)
        if (isWeak) {
            outMid.addScaledVector(radialDir, Math.min(72, dist * 0.22 + 16))
        } else {
            const tangent = new THREE.Vector3(-radialDir.y, radialDir.x, 0.35).normalize()
            outMid.addScaledVector(tangent, Math.min(48, dist * 0.16 + 10))
        }
    } else if (isCites) {
        outMid.z += Math.min(95, dist * 0.28 + 24)
    } else if (isWeak) {
        const perpX = -(pB.y - pA.y) * 0.14
        const perpY = (pB.x - pA.x) * 0.14
        outMid.x += perpX
        outMid.y += perpY
        outMid.z += 18
    } else {
        outMid.z += 4
    }
    return outMid
}

/** 容器内记忆囊泡的三维开普勒环流轨道参数 */
interface IntracellularOrbit {
    nodeId: number
    parentId: number
    radius: number
    yRatio: number
    radiusAtY: number
    baseTheta: number
    angularSpeed: number
    bobPhase: number
}

/** 围绕节点公转的一级量子原子微粒（Atom Satellite）与二级分子共价对 */
interface AtomicSatelliteOrbit {
    hostNodeId: number
    sprite: THREE.Sprite
    radius: number
    tiltX: number
    tiltY: number
    baseTheta: number
    angularSpeed: number
}

/** 分形生物闪电电弧（主电弧 + 外围辉光 + 树状分叉侧电弧 + 等离子电晕珠串） */
interface LightningBolt {
    coreLine: THREE.Line
    glowLine: THREE.Line
    branchLine: THREE.Line
    beadPoints: THREE.Points
    fromId: number
    toId: number
    pA: THREE.Vector3
    pB: THREE.Vector3
    bornAt: number
    delay: number
    duration: number
    jitterAmp: number
    lastJitterAt: number
    hop: 1 | 2
}

/** 容器去极化扩张冲击波光环 */
interface ShockwaveRing {
    mesh: THREE.Mesh
    bornAt: number
    delay: number
    duration: number
    startRadius: number
    endRadius: number
}

/** 生成一条带三维中点位移扰动与拱度隆起的分形生物闪电折线顶点 */
function computeJaggedLightningPoints(pA: THREE.Vector3, pB: THREE.Vector3, jitterAmp: number, segments = 18): THREE.Vector3[] {
    const pts: THREE.Vector3[] = []
    const dir = new THREE.Vector3().subVectors(pB, pA)
    const dist = Math.max(1, dir.length())
    dir.normalize()
    // 构造两个与连线正交的单位基向量 u, v
    const up = Math.abs(dir.z) < 0.9 ? new THREE.Vector3(0, 0, 1) : new THREE.Vector3(1, 0, 0)
    const u = new THREE.Vector3().crossVectors(dir, up).normalize()
    const v = new THREE.Vector3().crossVectors(dir, u).normalize()
    const archHeight = Math.min(48, dist * 0.16 + 8)

    for (let i = 0; i <= segments; i++) {
        const t = i / segments
        const base = new THREE.Vector3().lerpVectors(pA, pB, t)
        // 抛物线拱起 + 正弦包络锯齿分形位移（两端严格锚定在节点球心）
        const env = Math.sin(t * Math.PI)
        base.z += env * archHeight
        if (i > 0 && i < segments) {
            const amp = jitterAmp * env * (0.45 + 0.55 * Math.sin(i * 1.9))
            const r1 = (Math.random() - 0.5) * 2 * amp
            const r2 = (Math.random() - 0.5) * 2 * amp
            base.addScaledVector(u, r1)
            base.addScaledVector(v, r2)
        }
        pts.push(base)
    }
    return pts
}

/** 生成柔和的扩张冲击波圆环纹理 */
function createShockwaveRingTexture(): THREE.CanvasTexture {
    const size = 128
    const canvas = document.createElement('canvas')
    canvas.width = size
    canvas.height = size
    const ctx = canvas.getContext('2d')!
    const half = size / 2
    const grad = ctx.createRadialGradient(half, half, half * 0.45, half, half, half)
    grad.addColorStop(0.0, 'rgba(34,211,238,0)')
    grad.addColorStop(0.55, 'rgba(56,189,248,0.18)')
    grad.addColorStop(0.82, 'rgba(165,243,252,0.92)')
    grad.addColorStop(0.94, 'rgba(236,72,153,0.45)')
    grad.addColorStop(1.0, 'rgba(0,0,0,0)')
    ctx.fillStyle = grad
    ctx.fillRect(0, 0, size, size)
    const tex = new THREE.CanvasTexture(canvas)
    tex.needsUpdate = true
    return tex
}

export class Hypergraph3DEngine {
    private container: HTMLElement
    private callbacks: Graph3DCallbacks
    private scene: THREE.Scene
    private camera: THREE.PerspectiveCamera
    private renderer: THREE.WebGLRenderer
    private controls: OrbitControls

    private rootGroup = new THREE.Group()
    private somaGroup = new THREE.Group()
    private nodeGroup = new THREE.Group()
    private axonGroup = new THREE.Group()
    private labelGroup = new THREE.Group()
    private fxGroup = new THREE.Group()
    private nebulaCloudGroup = new THREE.Group()
    private polarGridGroup = new THREE.Group()
    private pulsePoints: THREE.Points | null = null
    private ambientStars: THREE.Points | null = null

    private pickableMeshes: THREE.Object3D[] = []
    private nodeByMeshId = new Map<number, Graph3DNode>()
    private nodeById = new Map<number, Graph3DNode>()
    private nodeKeyById = new Map<number, string>()
    private pos3DById = new Map<number, THREE.Vector3>()
    private targetPos3DById = new Map<number, THREE.Vector3>()
    private prevPos3DByKey = new Map<string, THREE.Vector3>()
    private nodeTransitions = new Map<number, NodeTransitionState>()
    private currentIsSphere = true
    private cellRadiusById = new Map<number, number>()
    private somaMaterials = new Map<number, THREE.ShaderMaterial>()
    private nodeCoreMeshes = new Map<number, THREE.Mesh>()
    private nodeHaloSprites = new Map<number, THREE.Sprite>()
    private nodeBaseScale = new Map<number, number>()
    private haloBaseScale = new Map<number, number>()
    private axonLines: {
        line: THREE.Line
        curve: THREE.QuadraticBezierCurve3
        segs: number
        from: number
        to: number
        relation: string
        baseOpacity: number
    }[] = []
    private pulseStreams: AxonPulseStream[] = []
    private intracellularOrbits: IntracellularOrbit[] = []
    private atomicSatellites: AtomicSatelliteOrbit[] = []
    private adjacencyMap = new Map<number, { peerId: number; relation: string }[]>()

    private lightningBolts: LightningBolt[] = []
    private shockwaves: ShockwaveRing[] = []
    private gyroRings: { line: THREE.Line; speedX: number; speedY: number; speedZ: number }[] = []
    private nodeSurgeUntil = new Map<number, { peak: number; until: number }>()
    private recentSeedIds: number[] = []
    private autoPulseCursor = 0
    private autoPulseEnabled = true
    private autoOrbitEnabled = true
    private lastAutoPulseAt = 0

    private glowTextures: Record<string, THREE.CanvasTexture> = {}
    private shockwaveTexture: THREE.CanvasTexture | null = null
    private raycaster = new THREE.Raycaster()
    private pointerNDC = new THREE.Vector2(-999, -999)
    private hoveredNodeId: number | null = null
    private selectedNodeId: number | null = null

    private animFrameId = 0
    private clock = new THREE.Clock()
    private disposed = false

    // 平滑相机飞行插值状态
    private flyAnim: {
        startPos: THREE.Vector3
        endPos: THREE.Vector3
        startTarget: THREE.Vector3
        endTarget: THREE.Vector3
        t0: number
        duration: number
    } | null = null

    private lastClickNodeId = -1
    private lastClickTime = 0
    private clickDelayTimer: ReturnType<typeof setTimeout> | null = null
    private pointerDownPos = { x: 0, y: 0 }

    constructor(container: HTMLElement, callbacks: Graph3DCallbacks) {
        this.container = container
        this.callbacks = callbacks

        const w = Math.max(320, container.clientWidth || 960)
        const h = Math.max(320, container.clientHeight || 620)

        this.scene = new THREE.Scene()
        this.camera = new THREE.PerspectiveCamera(48, w / h, 1, 12000)
        this.camera.position.set(0, -420, 480)
        this.camera.up.set(0, 0, 1)

        try {
            this.renderer = new THREE.WebGLRenderer({
                antialias: true,
                alpha: true,
                powerPreference: 'high-performance',
            })
        } catch {
            const fallbackCanvas = document.createElement('canvas')
            fallbackCanvas.width = w
            fallbackCanvas.height = h
            fallbackCanvas.setAttribute('data-webgl-fallback', '2d')
            this.renderer = {
                domElement: fallbackCanvas,
                sortObjects: true,
                setPixelRatio: () => {},
                setSize: (nw: number, nh: number) => {
                    fallbackCanvas.width = nw
                    fallbackCanvas.height = nh
                },
                render: () => {},
                dispose: () => {},
            } as unknown as THREE.WebGLRenderer
        }
        this.renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2))
        this.renderer.setSize(w, h)
        this.renderer.sortObjects = true

        container.innerHTML = ''
        container.appendChild(this.renderer.domElement)
        ;(container as any).__rm3dEngine = this

        this.controls = new OrbitControls(this.camera, this.renderer.domElement)
        this.controls.enableDamping = true
        this.controls.dampingFactor = 0.065
        this.controls.rotateSpeed = 0.72
        this.controls.zoomSpeed = 0.95
        this.controls.enableZoom = callbacks.enableZoom ?? true
        this.controls.panSpeed = 0.75
        this.controls.minDistance = 40
        this.controls.maxDistance = 4800
        this.controls.autoRotate = true
        this.controls.autoRotateSpeed = 0.38

        // 光影布置：柔和深空环境光 + 双色生物电点光源（强度克制，避免三层本色被冲白）
        const ambientLight = new THREE.AmbientLight(0xbfd7ef, 0.85)
        const keyLight = new THREE.PointLight(0x38bdf8, 1.25, 3200)
        keyLight.position.set(320, -280, 520)
        const rimLight = new THREE.PointLight(0xc084fc, 0.85, 3200)
        rimLight.position.set(-340, 320, 420)
        this.scene.add(ambientLight, keyLight, rimLight)

        this.rootGroup.add(this.axonGroup, this.somaGroup, this.nodeGroup, this.fxGroup, this.labelGroup)
        this.scene.add(this.nebulaCloudGroup, this.polarGridGroup, this.rootGroup)

        // 预生成四类柔和径向高斯光斑纹理（色相与三层认知调色板严格一致）
        this.glowTextures = {
            cell: createRadialGlowTexture('rgba(34,211,238,0.95)', 'rgba(8,145,178,0.38)', 'rgba(8,145,178,0)'),
            background: createRadialGlowTexture('rgba(59,130,246,0.95)', 'rgba(29,78,216,0.36)', 'rgba(29,78,216,0)'),
            episodic: createRadialGlowTexture('rgba(16,185,129,0.95)', 'rgba(4,120,87,0.36)', 'rgba(4,120,87,0)'),
            noetic: createRadialGlowTexture('rgba(236,72,153,0.98)', 'rgba(190,24,93,0.42)', 'rgba(190,24,93,0)'),
            focus: createRadialGlowTexture('rgba(251,191,36,0.98)', 'rgba(245,158,11,0.45)', 'rgba(245,158,11,0)'),
            nebulaBlue: createRadialGlowTexture('rgba(37,99,235,0.22)', 'rgba(14,116,144,0.08)', 'rgba(0,0,0,0)'),
            nebulaViolet: createRadialGlowTexture('rgba(147,51,234,0.18)', 'rgba(219,39,119,0.06)', 'rgba(0,0,0,0)'),
        }
        this.shockwaveTexture = createShockwaveRingTexture()

        this.createAmbientCosmicField()
        this.bindInteractionEvents()
        this.startAnimationLoop()
    }

    private createAmbientCosmicField(): void {
        const count = 1100
        const positions = new Float32Array(count * 3)
        const colors = new Float32Array(count * 3)
        const sizes = new Float32Array(count)
        const phases = new Float32Array(count)
        for (let i = 0; i < count; i++) {
            // 双层分布：内层悬浮量子生物微尘（r ∈ [180, 680]）+ 外层深空脉冲星（r ∈ [680, 2100]），采用确定性伪随机序列
            const isInnerSpore = i < 360
            const rRand = deterministicUnitRandom(i * 3 + 1)
            const tRand = deterministicUnitRandom(i * 3 + 2)
            const pRand = deterministicUnitRandom(i * 3 + 3)
            const r = isInnerSpore ? 180 + rRand * 500 : 680 + rRand * 1420
            const theta = tRand * Math.PI * 2
            const phi = Math.acos(2 * pRand - 1)
            positions[i * 3] = r * Math.sin(phi) * Math.cos(theta)
            positions[i * 3 + 1] = r * Math.sin(phi) * Math.sin(theta)
            positions[i * 3 + 2] = r * Math.cos(phi) * (isInnerSpore ? 0.42 : 0.6)

            const palette = ['#38bdf8', '#60a5fa', '#34d399', '#f472b6', '#a78bfa']
            const c = new THREE.Color(palette[i % palette.length]!)
            colors[i * 3] = c.r
            colors[i * 3 + 1] = c.g
            colors[i * 3 + 2] = c.b
            sizes[i] = isInnerSpore ? 3.5 + (i % 5) * 1.4 : 4.5 + (i % 7) * 1.8
            phases[i] = (i * 0.73) % (Math.PI * 2)
        }
        const geo = new THREE.BufferGeometry()
        geo.setAttribute('position', new THREE.BufferAttribute(positions, 3))
        geo.setAttribute('color', new THREE.BufferAttribute(colors, 3))
        geo.setAttribute('aSize', new THREE.BufferAttribute(sizes, 1))
        geo.setAttribute('aPhase', new THREE.BufferAttribute(phases, 1))
        const mat = new THREE.ShaderMaterial({
            vertexShader: STARFIELD_VERTEX_SHADER,
            fragmentShader: STARFIELD_FRAGMENT_SHADER,
            uniforms: {
                uTime: { value: 0 },
            },
            vertexColors: true,
            transparent: true,
            depthWrite: false,
            blending: THREE.AdditiveBlending,
        })
        this.ambientStars = new THREE.Points(geo, mat)
        this.scene.add(this.ambientStars)

        // 构建深渊级多色相星云气体云团（烘托深空银河氛围）
        const cloudLayout: { x: number; y: number; z: number; scale: number; tex: string; opacity: number }[] = [
            { x: -280, y: 160, z: -180, scale: 640, tex: 'nebulaBlue', opacity: 0.22 },
            { x: 310, y: -170, z: -160, scale: 580, tex: 'nebulaViolet', opacity: 0.2 },
            { x: 30, y: 260, z: -220, scale: 690, tex: 'nebulaBlue', opacity: 0.16 },
            { x: -220, y: -230, z: -170, scale: 540, tex: 'nebulaViolet', opacity: 0.18 },
            { x: 0, y: 0, z: 0, scale: 520, tex: 'nebulaViolet', opacity: 0.14 },
        ]
        for (const cl of cloudLayout) {
            const cMat = new THREE.SpriteMaterial({
                map: this.glowTextures[cl.tex],
                transparent: true,
                opacity: cl.opacity,
                depthWrite: false,
                blending: THREE.AdditiveBlending,
            })
            const sp = new THREE.Sprite(cMat)
            sp.position.set(cl.x, cl.y, cl.z)
            sp.scale.setScalar(cl.scale)
            sp.renderOrder = 0
            this.nebulaCloudGroup.add(sp)
        }
    }

    private bindInteractionEvents(): void {
        const dom = this.renderer.domElement

        dom.addEventListener('pointerdown', (ev: PointerEvent) => {
            this.pointerDownPos = { x: ev.clientX, y: ev.clientY }
            this.flyAnim = null
        })

        dom.addEventListener('pointermove', (ev: PointerEvent) => {
            const rect = dom.getBoundingClientRect()
            if (rect.width <= 0 || rect.height <= 0) return
            this.pointerNDC.x = ((ev.clientX - rect.left) / rect.width) * 2 - 1
            this.pointerNDC.y = -((ev.clientY - rect.top) / rect.height) * 2 + 1

            this.raycaster.setFromCamera(this.pointerNDC, this.camera)
            const hits = this.raycaster.intersectObjects(this.pickableMeshes, false)
            if (hits.length > 0) {
                const hitObj = hits[0]!.object
                const node = this.nodeByMeshId.get(hitObj.id) ?? null
                if (node) {
                    dom.style.cursor = 'pointer'
                    if (this.hoveredNodeId !== node.id) {
                        this.hoveredNodeId = node.id
                        this.updateHighlightState()
                        this.spawnTransientHoverLightning(node.id)
                    }
                    this.callbacks.onNodeHover(node, ev.clientX, ev.clientY)
                    return
                }
            }
            if (this.hoveredNodeId !== null) {
                this.hoveredNodeId = null
                this.updateHighlightState()
            }
            dom.style.cursor = 'grab'
            this.callbacks.onNodeHover(null, ev.clientX, ev.clientY)
        })

        dom.addEventListener('pointerleave', () => {
            if (this.hoveredNodeId !== null) {
                this.hoveredNodeId = null
                this.updateHighlightState()
            }
            this.callbacks.onNodeHover(null, 0, 0)
        })

        dom.addEventListener('pointerup', (ev: PointerEvent) => {
            const moveDist = Math.hypot(ev.clientX - this.pointerDownPos.x, ev.clientY - this.pointerDownPos.y)
            if (moveDist > 6) return

            const rect = dom.getBoundingClientRect()
            this.pointerNDC.x = ((ev.clientX - rect.left) / rect.width) * 2 - 1
            this.pointerNDC.y = -((ev.clientY - rect.top) / rect.height) * 2 + 1
            this.raycaster.setFromCamera(this.pointerNDC, this.camera)
            const hits = this.raycaster.intersectObjects(this.pickableMeshes, false)
            if (hits.length === 0) return
            const node = this.nodeByMeshId.get(hits[0]!.object.id)
            if (!node) return

            const now = performance.now()
            if (this.lastClickNodeId === node.id && now - this.lastClickTime <= 340) {
                this.lastClickNodeId = -1
                this.lastClickTime = 0
                if (this.clickDelayTimer) {
                    clearTimeout(this.clickDelayTimer)
                    this.clickDelayTimer = null
                }
                this.callbacks.onNodeDblClick(node)
                return
            }
            this.lastClickNodeId = node.id
            this.lastClickTime = now
            if (this.clickDelayTimer) clearTimeout(this.clickDelayTimer)
            this.clickDelayTimer = setTimeout(() => {
                this.clickDelayTimer = null
                this.triggerActivationCascade(node.id, true)
                this.callbacks.onNodeClick(node)
            }, 220)
        })
    }

    private clearGroup(g: THREE.Group): void {
        while (g.children.length > 0) {
            const ch = g.children[0]!
            g.remove(ch)
            const mesh = ch as THREE.Mesh
            if (mesh.geometry) mesh.geometry.dispose()
        }
    }

    /**
     * 将二维星系坐标 + 三层认知拓扑合成为真正的三维「记忆地址容器包络 × 容器内微粒 × 跨层突触轴突」空间拓扑
     */
    public setData(input: Graph3DRenderInput, forceFit = false): void {
        this.selectedNodeId = input.selectedNodeId
        this.clearGroup(this.somaGroup)
        this.clearGroup(this.nodeGroup)
        this.clearGroup(this.axonGroup)
        this.clearGroup(this.labelGroup)
        this.clearGroup(this.fxGroup)
        if (this.pulsePoints) {
            this.rootGroup.remove(this.pulsePoints)
            this.pulsePoints.geometry.dispose()
            this.pulsePoints = null
        }

        const prevSnapshot = new Map<string, THREE.Vector3>()
        for (const [k, v] of this.prevPos3DByKey.entries()) {
            prevSnapshot.set(k, v.clone())
        }

        this.pickableMeshes = []
        this.nodeByMeshId.clear()
        this.nodeById.clear()
        this.nodeKeyById.clear()
        this.pos3DById.clear()
        this.targetPos3DById.clear()
        this.nodeTransitions.clear()
        this.cellRadiusById.clear()
        this.somaMaterials.clear()
        this.nodeCoreMeshes.clear()
        this.nodeHaloSprites.clear()
        this.nodeBaseScale.clear()
        this.haloBaseScale.clear()
        this.axonLines = []
        this.pulseStreams = []
        this.intracellularOrbits = []
        this.atomicSatellites = []
        this.adjacencyMap.clear()
        this.lightningBolts = []
        this.shockwaves = []
        this.gyroRings = []
        this.nodeSurgeUntil.clear()

        const { nodes, edges, coords2D, inDrill, focusId, hopMap, showEdgeLabels, isDark } = input
        const isSphere = (input.spaceMode ?? 'sphere') === 'sphere'
        this.currentIsSphere = isSphere
        this.polarGridGroup.visible = !isSphere
        if (nodes.length === 0) return

        for (const n of nodes) {
            this.nodeById.set(n.id, n)
            this.nodeKeyById.set(n.id, n.node_key || String(n.id))
        }
        const addAdj = (a: number, b: number, rel: string) => {
            if (!this.adjacencyMap.has(a)) this.adjacencyMap.set(a, [])
            this.adjacencyMap.get(a)!.push({ peerId: b, relation: rel })
        }
        for (const e of edges) {
            addAdj(e.from, e.to, e.relation)
            addAdj(e.to, e.from, e.relation)
        }

        // 1. 解析 Cell 容器与下属 Unit 的归属关系（用于构建真实的容器内部三维微粒包络），以及 Atom/Molecule 的宿主归属
        const cellMembersMap = new Map<number, number[]>()
        const unitParentCell = new Map<number, number>()
        const particleHostNode = new Map<number, number>()
        for (const e of edges) {
            if (e.relation === 'member_of') {
                if (!cellMembersMap.has(e.to)) cellMembersMap.set(e.to, [])
                cellMembersMap.get(e.to)!.push(e.from)
                unitParentCell.set(e.from, e.to)
            } else if ((e.relation === 'atom_of' || e.relation === 'molecule_of') && !particleHostNode.has(e.from)) {
                particleHostNode.set(e.from, e.to)
            }
        }

        // 2. 计算每个节点的三维空间坐标 (X, Y, Z)
        //    - 在全景模式下：
        //      • Cell 容器母体分布在主星云层（按哈希给微起伏 Z ∈ [-35, +35]）；
        //      • 属于某 Cell 的 L1/L2 Unit 微粒严格环绕在所属 Cell 球心内部的斐波那契三维内轨上（真正被包裹在光晕球壳内！）；
        //      • L3 Noetic 知性神经节悬浮在星云上层天穹（Z ∈ [+75, +135]），向下方容器发射跨层神经脉冲束！
        //    - 在下钻模式（inDrill）下：
        //      • 焦点位于 (0, 0, 0)，1/2/3 跳邻居分布在三维立体同心轨道壳上。
        const goldenAngle = Math.PI * (3 - Math.sqrt(5))

        // 黄金球面斐波那契采样器：将第 idx / total 个节点均匀放置在半径 R 的真三维球面上（X² + Y² + Z² = R²）
        const fibonacciSpherePoint = (idx: number, total: number, radius: number, phaseSeed = 0): THREE.Vector3 => {
            if (total <= 1) {
                return new THREE.Vector3(0, 0, radius * 0.35)
            }
            const y = 1 - ((idx + 0.5) / total) * 2
            const rAtY = Math.sqrt(Math.max(0.02, 1 - y * y))
            const theta = idx * goldenAngle + phaseSeed
            return new THREE.Vector3(Math.cos(theta) * rAtY * radius, Math.sin(theta) * rAtY * radius, y * radius)
        }

        if (isSphere) {
            // ==================== 模式 A：真 3D 球形宇宙模式（True 3D Spherical Universe） ====================
            if (inDrill) {
                // 下钻模式：焦点位于球心 (0,0,0)，1/2/3 跳邻居分布在真三维同心球壳上
                const hopBuckets = new Map<number, Graph3DNode[]>()
                for (const n of nodes) {
                    if (n.id === focusId) {
                        this.pos3DById.set(n.id, new THREE.Vector3(0, 0, 0))
                        if (n.kind === 'cell') this.cellRadiusById.set(n.id, 24)
                        continue
                    }
                    const h = hopMap[n.id] ?? 3
                    if (!hopBuckets.has(h)) hopBuckets.set(h, [])
                    hopBuckets.get(h)!.push(n)
                    if (n.kind === 'cell') this.cellRadiusById.set(n.id, 20)
                }
                for (const [hop, list] of hopBuckets.entries()) {
                    const shellR = hop === 1 ? 145 : hop === 2 ? 265 : 375
                    list.forEach((n, i) => {
                        this.pos3DById.set(n.id, fibonacciSpherePoint(i, list.length, shellR, hop * 1.7))
                    })
                }
            } else {
                // 全景球形宇宙：
                // • 内核恒星层（R ∈ [55, 135]）：L3 Noetic 知性神经节悬浮于球体核心；
                // • 中层情景球壳（R ∈ [195, 295]）：L2 Episodic 情景 Cell 星云团分布在三维中层球面上；
                // • 外层背景球壳（R ∈ [315, 415]）：L1 Background 背景 Cell 星云团分布在三维外层球面上。
                const noeticList = nodes.filter((n) => n.kind !== 'cell' && n.layer === 'noetic')
                const epCells = nodes.filter((n) => n.kind === 'cell' && n.layer !== 'background')
                const bgCells = nodes.filter((n) => n.kind === 'cell' && n.layer === 'background')

                noeticList.forEach((n, i) => {
                    const r = 62 + (i % 4) * 22
                    this.pos3DById.set(n.id, fibonacciSpherePoint(i, Math.max(1, noeticList.length), r, 0.4))
                })
                epCells.forEach((n, i) => {
                    const memberIds = cellMembersMap.get(n.id) ?? []
                    const somaRad = Math.min(54, 18 + Math.sqrt(memberIds.length) * 8.2)
                    this.cellRadiusById.set(n.id, somaRad)
                    const r = 210 + ((n.id % 5) - 2) * 18
                    this.pos3DById.set(n.id, fibonacciSpherePoint(i, Math.max(1, epCells.length), r, 1.3))
                })
                bgCells.forEach((n, i) => {
                    const memberIds = cellMembersMap.get(n.id) ?? []
                    const somaRad = Math.min(54, 18 + Math.sqrt(memberIds.length) * 8.2)
                    this.cellRadiusById.set(n.id, somaRad)
                    const r = 345 + ((n.id % 5) - 2) * 16
                    this.pos3DById.set(n.id, fibonacciSpherePoint(i, Math.max(1, bgCells.length), r, 2.6))
                })

                // 构建真球形宇宙「双层 3D FBM 分形体积星云包络场（Volumetric Cosmic Nebula Core & Envelope）」+ 900 颗球面旋臂星尘流（零机械铁丝圈！）
                const nebulaCoreMat = new THREE.ShaderMaterial({
                    vertexShader: SOMA_MEMBRANE_VERTEX_SHADER,
                    fragmentShader: SOMA_MEMBRANE_FRAGMENT_SHADER,
                    uniforms: {
                        uTime: { value: 0 },
                        uPulsePhase: { value: 1.2 },
                        uColor: { value: new THREE.Color('#701a75') },
                        uRimColor: { value: new THREE.Color('#ec4899') },
                        uOpacity: { value: 0.12 },
                        uHighlight: { value: 0.0 },
                    },
                    transparent: true,
                    depthWrite: false,
                    side: THREE.DoubleSide,
                    blending: THREE.AdditiveBlending,
                })
                const nebulaCoreMesh = new THREE.Mesh(new THREE.SphereGeometry(1, 36, 28), nebulaCoreMat)
                nebulaCoreMesh.scale.setScalar(155)
                nebulaCoreMesh.renderOrder = 1
                this.somaGroup.add(nebulaCoreMesh)
                this.somaMaterials.set(-9001, nebulaCoreMat)

                const nebulaOuterMat = new THREE.ShaderMaterial({
                    vertexShader: SOMA_MEMBRANE_VERTEX_SHADER,
                    fragmentShader: SOMA_MEMBRANE_FRAGMENT_SHADER,
                    uniforms: {
                        uTime: { value: 0 },
                        uPulsePhase: { value: 3.7 },
                        uColor: { value: new THREE.Color('#0c4a6e') },
                        uRimColor: { value: new THREE.Color('#06b6d4') },
                        uOpacity: { value: 0.09 },
                        uHighlight: { value: 0.0 },
                    },
                    transparent: true,
                    depthWrite: false,
                    side: THREE.DoubleSide,
                    blending: THREE.AdditiveBlending,
                })
                const nebulaOuterMesh = new THREE.Mesh(new THREE.SphereGeometry(1, 36, 28), nebulaOuterMat)
                nebulaOuterMesh.scale.setScalar(425)
                nebulaOuterMesh.renderOrder = 1
                this.somaGroup.add(nebulaOuterMesh)
                this.somaMaterials.set(-9002, nebulaOuterMat)

                // 900 颗沿三维球面螺旋旋臂流淌的吸积星尘群（采用确定性伪随机序列，消除刷新跳变）
                const armCount = 900
                const armPos = new Float32Array(armCount * 3)
                const armCol = new Float32Array(armCount * 3)
                const armSize = new Float32Array(armCount)
                const armPhase = new Float32Array(armCount)
                const armPalette = ['#38bdf8', '#22d3ee', '#34d399', '#f472b6', '#818cf8']
                for (let i = 0; i < armCount; i++) {
                    const u = i / armCount
                    const armIdx = i % 3
                    const lat = (u - 0.5) * Math.PI * 0.92
                    const lonJitter = (deterministicUnitRandom(i * 3 + 101) - 0.5) * 0.42
                    const rJitter = (deterministicUnitRandom(i * 3 + 102) - 0.5) * 38
                    const zJitter = (deterministicUnitRandom(i * 3 + 103) - 0.5) * 28
                    const lon = u * Math.PI * 6.5 + (armIdx * Math.PI * 2) / 3 + lonJitter
                    const r = 120 + u * 310 + rJitter
                    armPos[i * 3] = r * Math.cos(lat) * Math.cos(lon)
                    armPos[i * 3 + 1] = r * Math.cos(lat) * Math.sin(lon)
                    armPos[i * 3 + 2] = r * Math.sin(lat) + zJitter
                    const c = new THREE.Color(armPalette[i % armPalette.length]!)
                    armCol[i * 3] = c.r
                    armCol[i * 3 + 1] = c.g
                    armCol[i * 3 + 2] = c.b
                    armSize[i] = 2.8 + (i % 5) * 1.15
                    armPhase[i] = i * 0.37
                }
                const armGeo = new THREE.BufferGeometry()
                armGeo.setAttribute('position', new THREE.BufferAttribute(armPos, 3))
                armGeo.setAttribute('color', new THREE.BufferAttribute(armCol, 3))
                armGeo.setAttribute('aSize', new THREE.BufferAttribute(armSize, 1))
                armGeo.setAttribute('aPhase', new THREE.BufferAttribute(armPhase, 1))
                const armMat = new THREE.ShaderMaterial({
                    vertexShader: STARFIELD_VERTEX_SHADER,
                    fragmentShader: STARFIELD_FRAGMENT_SHADER,
                    uniforms: { uTime: { value: 0 } },
                    vertexColors: true,
                    transparent: true,
                    depthWrite: false,
                    blending: THREE.AdditiveBlending,
                })
                const armPoints = new THREE.Points(armGeo, armMat)
                armPoints.renderOrder = 2
                this.somaGroup.add(armPoints)
            }
        } else {
            // ==================== 模式 B：展开星盘模式（Unfolded Spiral Galaxy Disc · 原始紧凑三层 + 正圆等向同性校准） ====================
            // 构建 750 颗双旋臂银河星盘微尘流（Barred Spiral Galaxy Stardust Disc，采用确定性伪随机序列）
            const discCount = 750
            const dPos = new Float32Array(discCount * 3)
            const dCol = new Float32Array(discCount * 3)
            const dSize = new Float32Array(discCount)
            const dPhase = new Float32Array(discCount)
            const dPalette = ['#38bdf8', '#10b981', '#60a5fa', '#ec4899']
            for (let i = 0; i < discCount; i++) {
                const u = Math.pow(i / discCount, 0.72)
                const r = 45 + u * 580
                const arm = (i % 2) * Math.PI
                const angleJitter = (deterministicUnitRandom(i * 2 + 5001) - 0.5) * 0.55
                const zJitter = (deterministicUnitRandom(i * 2 + 5002) - 0.5) * (36 * (1 - u * 0.6))
                const angle = u * Math.PI * 4.2 + arm + angleJitter
                dPos[i * 3] = Math.cos(angle) * r
                dPos[i * 3 + 1] = Math.sin(angle) * r
                dPos[i * 3 + 2] = zJitter
                const c = new THREE.Color(dPalette[i % dPalette.length]!)
                dCol[i * 3] = c.r
                dCol[i * 3 + 1] = c.g
                dCol[i * 3 + 2] = c.b
                dSize[i] = 2.6 + (i % 4) * 1.1
                dPhase[i] = i * 0.41
            }
            const dGeo = new THREE.BufferGeometry()
            dGeo.setAttribute('position', new THREE.BufferAttribute(dPos, 3))
            dGeo.setAttribute('color', new THREE.BufferAttribute(dCol, 3))
            dGeo.setAttribute('aSize', new THREE.BufferAttribute(dSize, 1))
            dGeo.setAttribute('aPhase', new THREE.BufferAttribute(dPhase, 1))
            const dMat = new THREE.ShaderMaterial({
                vertexShader: STARFIELD_VERTEX_SHADER,
                fragmentShader: STARFIELD_FRAGMENT_SHADER,
                uniforms: { uTime: { value: 0 } },
                vertexColors: true,
                transparent: true,
                depthWrite: false,
                blending: THREE.AdditiveBlending,
            })
            const discPoints = new THREE.Points(dGeo, dMat)
            discPoints.renderOrder = 1
            this.somaGroup.add(discPoints)

            // 消除 2D 布局因宽屏 aspect (x * √aspect, y / √aspect) 造成的 Y 轴压扁椭圆变形：
            // 分别统计 X 与 Y 的 P90 半轴跨度，还原为等向同性的正圆星盘（X 跨度 ≈ Y 跨度），并柔和收敛外围离群远点
            const absX: number[] = []
            const absY: number[] = []
            for (const n of nodes) {
                const p2 = coords2D[n.id]
                if (!p2) continue
                if (Math.abs(p2[0]) > 1) absX.push(Math.abs(p2[0]))
                if (Math.abs(p2[1]) > 1) absY.push(Math.abs(p2[1]))
            }
            absX.sort((a, b) => a - b)
            absY.sort((a, b) => a - b)
            const spanX90 = absX.length > 4 ? absX[Math.floor(absX.length * 0.9)]! : 320
            const spanY90 = absY.length > 4 ? absY[Math.floor(absY.length * 0.9)]! : 320
            const TARGET_DISC_R = 460
            const scaleX = inDrill ? 1.0 : TARGET_DISC_R / Math.max(120, spanX90)
            const scaleY = inDrill ? 1.0 : TARGET_DISC_R / Math.max(120, spanY90)

            const mapDiscXY = (rawX: number, rawY: number, mul = 1.0): [number, number] => {
                if (inDrill) return [rawX, rawY]
                let x = rawX * scaleX * mul
                let y = rawY * scaleY * mul
                const r = Math.hypot(x, y)
                const softCap = 540 * mul
                if (r > softCap) {
                    const compressedR = softCap + Math.tanh((r - softCap) / 260) * 95
                    const ratio = compressedR / r
                    x *= ratio
                    y *= ratio
                }
                return [x, y]
            }

            // 严格采用原始版本（7c90d7e）的三层紧凑 Z 轴分布：
            // • Cell 容器母体共处于星盘主层（Z ∈ [-37.8, +37.8] 微起伏）；
            // • L3 Noetic 知性神经节紧贴在星盘上方（Z ∈ [78, 122]），向下方容器发射跨层神经脉冲束。
            for (const n of nodes) {
                const p2 = coords2D[n.id] ?? [0, 0]
                if (inDrill) {
                    const hop = hopMap[n.id] ?? 3
                    const zWave = n.id === focusId ? 0 : ((n.id % 11) - 5) * (hop === 1 ? 8 : 14)
                    this.pos3DById.set(n.id, new THREE.Vector3(p2[0], p2[1], zWave))
                    if (n.kind === 'cell') {
                        this.cellRadiusById.set(n.id, 22)
                    }
                    continue
                }

                if (n.kind === 'cell') {
                    const memberIds = cellMembersMap.get(n.id) ?? []
                    const somaRad = Math.min(64, 22 + Math.sqrt(memberIds.length) * 9.5)
                    this.cellRadiusById.set(n.id, somaRad)
                    const [dx, dy] = mapDiscXY(p2[0], p2[1], 1.0)
                    const zOffset = (((n.id * 31) % 19) - 9) * 4.2
                    this.pos3DById.set(n.id, new THREE.Vector3(dx, dy, zOffset))
                } else if (n.layer === 'noetic') {
                    const [dx, dy] = mapDiscXY(p2[0], p2[1], 0.92)
                    const zSky = 78 + ((n.id * 17) % 11) * 4.0
                    this.pos3DById.set(n.id, new THREE.Vector3(dx, dy, zSky))
                }
            }

            // 星盘防重叠弹性松弛（Disc Collision Relaxation）：
            // 对主星盘层的所有 Cell 容器做 8 轮轻量级排斥松弛，确保相邻半透明光晕球壳（somaRad）舒展不重叠
            if (!inDrill) {
                const discCells = nodes.filter((n) => n.kind === 'cell' && this.pos3DById.has(n.id))
                for (let pass = 0; pass < 8; pass++) {
                    for (let i = 0; i < discCells.length; i++) {
                        const ci = discCells[i]!
                        const pi = this.pos3DById.get(ci.id)!
                        const ri = (this.cellRadiusById.get(ci.id) ?? 24) + 12
                        for (let j = i + 1; j < discCells.length; j++) {
                            const cj = discCells[j]!
                            const pj = this.pos3DById.get(cj.id)!
                            const rj = (this.cellRadiusById.get(cj.id) ?? 24) + 12
                            const minDist = ri + rj
                            let dx = pj.x - pi.x
                            let dy = pj.y - pi.y
                            const distSq = dx * dx + dy * dy
                            if (distSq < minDist * minDist) {
                                const dist = Math.sqrt(distSq) || 0.1
                                const push = ((minDist - dist) / dist) * 0.42
                                dx *= push
                                dy *= push
                                pi.x -= dx
                                pi.y -= dy
                                pj.x += dx
                                pj.y += dy
                            }
                        }
                    }
                }
            }
        }

        // 再定锚所有归属于 Cell 的容器内 Unit 微粒（分布在 Cell 光晕球壳内部的 3D 斐波那契球面轨道上），以及 Atom/Molecule 一等粒子节点
        let orphanIdx = 0
        let particleIdx = 0
        for (const n of nodes) {
            if (this.pos3DById.has(n.id)) continue
            if (n.kind === 'atom' || n.kind === 'molecule') {
                particleIdx++
                const hostId = particleHostNode.get(n.id)
                const hostPos = hostId !== undefined ? this.pos3DById.get(hostId) : undefined
                if (!inDrill && hostPos) {
                    const isAtom = n.kind === 'atom'
                    const orbitR = isAtom ? 32 + (particleIdx % 4) * 9 : 46 + (particleIdx % 3) * 11
                    const th = particleIdx * goldenAngle * 1.7
                    const zOff = (isAtom ? 26 : -28) + ((particleIdx % 5) - 2) * 7
                    this.pos3DById.set(
                        n.id,
                        new THREE.Vector3(hostPos.x + Math.cos(th) * orbitR, hostPos.y + Math.sin(th) * orbitR, hostPos.z + zOff)
                    )
                } else if (isSphere) {
                    const shellR = n.kind === 'atom' ? 165 : 285
                    this.pos3DById.set(n.id, fibonacciSpherePoint(particleIdx, 40, shellR, 2.1))
                } else {
                    const p2 = coords2D[n.id] ?? [0, 0]
                    const zParticle = n.kind === 'atom' ? 48 : -44
                    this.pos3DById.set(n.id, new THREE.Vector3(p2[0], p2[1], zParticle))
                }
                continue
            }
            const parentId = unitParentCell.get(n.id)
            if (!inDrill && parentId !== undefined && this.pos3DById.has(parentId)) {
                const cellCenter = this.pos3DById.get(parentId)!
                const somaRad = this.cellRadiusById.get(parentId) ?? 28
                const siblings = cellMembersMap.get(parentId) ?? [n.id]
                const idx = Math.max(0, siblings.indexOf(n.id))
                const total = Math.max(1, siblings.length)

                // 斐波那契球面均匀分布，半径控制在 0.52 ~ 0.86 * somaRad，使三层发光记忆微粒舒展不重叠
                const y = total === 1 ? 0 : 1 - (idx / (total - 1)) * 1.65
                const radiusAtY = Math.sqrt(Math.max(0.08, 1 - y * y))
                const theta = idx * goldenAngle + (parentId % 7) * 0.5
                const innerR = somaRad * (total === 1 ? 0.55 : 0.52 + (idx % 3) * 0.15)

                const ux = cellCenter.x + Math.cos(theta) * radiusAtY * innerR
                const uy = cellCenter.y + Math.sin(theta) * radiusAtY * innerR
                const uz = cellCenter.z + y * innerR * 0.85
                this.pos3DById.set(n.id, new THREE.Vector3(ux, uy, uz))
                this.intracellularOrbits.push({
                    nodeId: n.id,
                    parentId,
                    radius: innerR,
                    yRatio: y * 0.85,
                    radiusAtY,
                    baseTheta: theta,
                    angularSpeed: (idx % 2 === 0 ? 1 : -1) * (0.2 + (n.id % 7) * 0.025),
                    bobPhase: (n.id % 19) * 0.33,
                })
            } else if (isSphere) {
                orphanIdx++
                const shellR = n.layer === 'background' ? 350 : 235
                this.pos3DById.set(n.id, fibonacciSpherePoint(orphanIdx, 48, shellR, 3.1))
            } else {
                const p2 = coords2D[n.id] ?? [0, 0]
                const zBase = n.layer === 'background' ? -24 : 0
                this.pos3DById.set(n.id, new THREE.Vector3(p2[0] * 1.0, p2[1] * 1.0, zBase + ((n.id % 9) - 4) * 4))
            }
        }

        // 2.5 记录目标三维坐标，并基于 prevPos3DByKey 规划「存量节点平滑插值」与「新节点/容器 0->1 生长动画（0.6s）」
        const nowElapsed = this.clock.getElapsedTime()
        const TRANSITION_DURATION = 0.6
        for (const [id, targetPos] of this.pos3DById.entries()) {
            this.targetPos3DById.set(id, targetPos.clone())
        }

        const startPosById = new Map<number, THREE.Vector3>()
        const isNewById = new Map<number, boolean>()
        let newCount = 0
        for (const n of nodes) {
            const targetPos = this.targetPos3DById.get(n.id)
            if (!targetPos) continue
            const nKey = this.nodeKeyById.get(n.id) ?? (n.node_key || String(n.id))
            const prevPos = prevSnapshot.get(nKey)
            if (prevPos) {
                startPosById.set(n.id, prevPos.clone())
                isNewById.set(n.id, false)
            } else {
                isNewById.set(n.id, true)
                newCount++
                const parentId = unitParentCell.get(n.id)
                let spawnOrigin = new THREE.Vector3(0, 0, 0)
                if (parentId !== undefined) {
                    const parentKey = this.nodeKeyById.get(parentId)
                    const parentPrev = parentKey ? prevSnapshot.get(parentKey) : undefined
                    const parentTarget = this.targetPos3DById.get(parentId)
                    if (parentPrev) {
                        spawnOrigin = parentPrev.clone()
                    } else if (parentTarget) {
                        spawnOrigin = parentTarget.clone()
                    }
                }
                startPosById.set(n.id, spawnOrigin)
            }
            this.prevPos3DByKey.set(nKey, targetPos.clone())
        }

        // 3. 构建 Cell 光晕球壳（Fresnel Soma Membrane）+ 发光核心 + 三层记忆发光囊泡
        const sphereGeo = new THREE.SphereGeometry(1, 32, 24)
        const coreGeo = new THREE.SphereGeometry(1, 20, 16)

        // 统计需要常显胶囊标签的 Top-14 核心 Cell 与 Top-12 L3 Noetic 神经节（避免 100+ 标签糊满全屏）
        const topCellIds = new Set(
            nodes
                .filter((x) => x.kind === 'cell' && (cellMembersMap.get(x.id)?.length ?? 0) >= 2)
                .sort((a, b) => (cellMembersMap.get(b.id)?.length ?? 0) - (cellMembersMap.get(a.id)?.length ?? 0) || b.viewDegree - a.viewDegree)
                .slice(0, 14)
                .map((x) => x.id)
        )
        const topNoeticIds = new Set(
            nodes
                .filter((x) => x.kind !== 'cell' && x.layer === 'noetic')
                .sort((a, b) => b.viewDegree - a.viewDegree)
                .slice(0, 12)
                .map((x) => x.id)
        )

        let spawnedShockwaves = 0
        for (const n of nodes) {
            const targetPos = this.targetPos3DById.get(n.id)
            if (!targetPos) continue
            const startPos = startPosById.get(n.id) ?? targetPos.clone()
            const isNew = isNewById.get(n.id) ?? false
            const initSpawnScale = isNew ? 0.001 : 1.0
            this.pos3DById.set(n.id, startPos.clone())

            const isCell = n.kind === 'cell'
            const isNoetic = !isCell && n.layer === 'noetic'
            const isFocus = inDrill && focusId === n.id
            const somaRad = this.cellRadiusById.get(n.id) ?? 22
            let membraneMeshRef: THREE.Mesh | undefined
            let plumeSpriteRef: THREE.Sprite | undefined
            let labelSpriteRef: THREE.Sprite | undefined
            let labelZLiftVal = 0

            if (isCell) {
                const memberCount = cellMembersMap.get(n.id)?.length ?? 0
                // 仅当 Cell 收纳 >= 2 个可见成员（或处于焦点/选中态）时才渲染外层菲涅尔光晕球壳；
                // 球壳色相与容器主导层联动（L1 背景容器呈现深海电光蓝，L2 情景容器呈现生物翡翠青绿）
                const isBgCell = n.layer === 'background'
                const showMembrane = memberCount >= 2 || isFocus || this.selectedNodeId === n.id
                if (showMembrane) {
                    const membraneColor = new THREE.Color(isBgCell ? '#1d4ed8' : '#0d9488')
                    const rimColor = new THREE.Color(isBgCell ? '#60a5fa' : '#2dd4bf')
                    const somaMat = new THREE.ShaderMaterial({
                        vertexShader: SOMA_MEMBRANE_VERTEX_SHADER,
                        fragmentShader: SOMA_MEMBRANE_FRAGMENT_SHADER,
                        uniforms: {
                            uTime: { value: 0 },
                            uPulsePhase: { value: (n.id % 37) * 0.17 },
                            uColor: { value: membraneColor },
                            uRimColor: { value: rimColor },
                            uOpacity: { value: 0.22 },
                            uHighlight: { value: isFocus || this.selectedNodeId === n.id ? 1.0 : 0.0 },
                        },
                        transparent: true,
                        depthWrite: false,
                        side: THREE.DoubleSide,
                        blending: THREE.AdditiveBlending,
                    })
                    const membraneMesh = new THREE.Mesh(sphereGeo, somaMat)
                    membraneMesh.position.copy(startPos)
                    membraneMesh.scale.setScalar(somaRad * initSpawnScale)
                    membraneMesh.renderOrder = 2
                    this.somaGroup.add(membraneMesh)
                    this.somaMaterials.set(n.id, somaMat)
                    membraneMeshRef = membraneMesh

                    // A2. 柔和有机星云气体外晕（Nebula Gas Plume，取代机械铁丝圆环）
                    const plumeMat = new THREE.SpriteMaterial({
                        map: isBgCell ? this.glowTextures.nebulaBlue : this.glowTextures.cell,
                        transparent: true,
                        opacity: 0.34,
                        depthWrite: false,
                        blending: THREE.AdditiveBlending,
                    })
                    const plume = new THREE.Sprite(plumeMat)
                    plume.position.copy(startPos)
                    plume.scale.setScalar(somaRad * 2.1 * initSpawnScale)
                    plume.renderOrder = 1
                    this.somaGroup.add(plume)
                    plumeSpriteRef = plume
                }

                // B. 容器中心发光核心（Soma Nucleus，作为 Raycaster 拾取核心）
                const nucleusMat = new THREE.MeshPhysicalMaterial({
                    color: isFocus ? '#f59e0b' : '#22d3ee',
                    emissive: isFocus ? '#b45309' : '#0891b2',
                    emissiveIntensity: isFocus ? 0.75 : 0.5,
                    roughness: 0.2,
                    metalness: 0.1,
                    transparent: true,
                    opacity: 0.92,
                })
                const nucleusScale = isFocus ? 7.0 : memberCount >= 2 ? 4.2 : 3.0
                const nucleusMesh = new THREE.Mesh(coreGeo, nucleusMat)
                nucleusMesh.position.copy(startPos)
                nucleusMesh.scale.setScalar(nucleusScale * initSpawnScale)
                nucleusMesh.renderOrder = 5
                this.nodeGroup.add(nucleusMesh)
                this.pickableMeshes.push(nucleusMesh)
                this.nodeByMeshId.set(nucleusMesh.id, n)
                this.nodeCoreMeshes.set(n.id, nucleusMesh)
                this.nodeBaseScale.set(n.id, nucleusScale)

                // C. 紧凑核心柔光晕（仅包覆发光核心自身，不再 2.35× 放大吞没内部三层记忆）
                const haloScale = isFocus ? 28 : memberCount >= 2 ? 15 : 9
                const haloMat = new THREE.SpriteMaterial({
                    map: isFocus ? this.glowTextures.focus : this.glowTextures.cell,
                    transparent: true,
                    opacity: isFocus ? 0.65 : 0.28,
                    depthWrite: false,
                    blending: THREE.AdditiveBlending,
                })
                const halo = new THREE.Sprite(haloMat)
                halo.position.copy(startPos)
                halo.scale.setScalar(haloScale * initSpawnScale)
                halo.renderOrder = 1
                this.somaGroup.add(halo)
                this.nodeHaloSprites.set(n.id, halo)
                this.haloBaseScale.set(n.id, haloScale)
            } else {
                // Unit 记忆囊泡 / L3 Noetic 神经节 / Atom 原子荷 / Molecule 分子波包
                const isAtom = n.kind === 'atom'
                const isMolecule = n.kind === 'molecule'
                const baseCol = isFocus
                    ? '#f59e0b'
                    : isAtom
                      ? '#f59e0b'
                      : isMolecule
                        ? '#a855f7'
                        : isNoetic
                          ? '#ec4899'
                          : n.layer === 'background'
                            ? '#3b82f6'
                            : '#10b981'
                const emissiveCol = isFocus
                    ? '#d97706'
                    : isAtom
                      ? '#d97706'
                      : isMolecule
                        ? '#7e22ce'
                        : isNoetic
                          ? '#db2777'
                          : n.layer === 'background'
                            ? '#2563eb'
                            : '#059669'

                const coreRadius = isFocus ? 7.5 : isAtom ? 4.4 : isMolecule ? 4.9 : isNoetic ? 5.8 : 5.2
                const unitMat = new THREE.MeshStandardMaterial({
                    color: baseCol,
                    emissive: emissiveCol,
                    emissiveIntensity: isAtom || isMolecule ? 0.78 : isNoetic ? 0.65 : 0.62,
                    roughness: 0.18,
                    metalness: 0.08,
                })
                const unitMesh = new THREE.Mesh(coreGeo, unitMat)
                unitMesh.position.copy(startPos)
                unitMesh.scale.setScalar(coreRadius * initSpawnScale)
                unitMesh.renderOrder = 6
                this.nodeGroup.add(unitMesh)
                this.pickableMeshes.push(unitMesh)
                this.nodeByMeshId.set(unitMesh.id, n)
                this.nodeCoreMeshes.set(n.id, unitMesh)
                this.nodeBaseScale.set(n.id, coreRadius)

                const texKey = isFocus || isAtom ? 'focus' : isMolecule || isNoetic ? 'noetic' : n.layer === 'background' ? 'background' : 'episodic'
                const haloScale = coreRadius * (isAtom || isMolecule ? 4.4 : isNoetic ? 4.2 : 5.2)
                const haloMat = new THREE.SpriteMaterial({
                    map: this.glowTextures[texKey],
                    transparent: true,
                    opacity: isNoetic ? 0.72 : 0.88,
                    depthWrite: false,
                    blending: THREE.AdditiveBlending,
                })
                const halo = new THREE.Sprite(haloMat)
                halo.position.copy(startPos)
                halo.scale.setScalar(haloScale * initSpawnScale)
                halo.renderOrder = 4
                this.nodeGroup.add(halo)
                this.nodeHaloSprites.set(n.id, halo)
                this.haloBaseScale.set(n.id, haloScale)
            }

            // 3.8 为焦点、选中或高能核心节点构建「量子原子轨道微粒（Atomic Quantum Satellites）」与「分子共价轨道环（Molecular Orbital Ring）」
            const showAtomicOrbitals =
                (isFocus || n.id === this.selectedNodeId || topCellIds.has(n.id) || topNoeticIds.has(n.id)) &&
                Array.isArray(n.atoms) &&
                n.atoms.length > 0
            if (showAtomicOrbitals) {
                const topA = (n.atoms ?? []).slice(0, isCell ? 5 : 4)
                const baseOrbitR = isCell ? somaRad * 1.12 : isFocus ? 14.5 : 10.5
                topA.forEach((atom, aIdx) => {
                    const satMat = new THREE.SpriteMaterial({
                        map: aIdx % 2 === 0 ? this.glowTextures.focus : this.glowTextures.noetic,
                        transparent: true,
                        opacity: 0.92,
                        depthWrite: false,
                        blending: THREE.AdditiveBlending,
                    })
                    const satSprite = new THREE.Sprite(satMat)
                    const potScale = Math.min(7.5, Math.max(3.6, 3.2 + (atom.potential || 1.5) * 0.55))
                    satSprite.scale.setScalar(potScale)
                    satSprite.position.copy(startPos)
                    satSprite.renderOrder = 9
                    this.nodeGroup.add(satSprite)
                    this.atomicSatellites.push({
                        hostNodeId: n.id,
                        sprite: satSprite,
                        radius: baseOrbitR + (aIdx % 2) * 3.2,
                        tiltX: ((aIdx * 1.17 + (n.id % 7) * 0.4) % 1.4) - 0.7,
                        tiltY: ((aIdx * 0.83 + (n.id % 5) * 0.5) % 1.4) - 0.7,
                        baseTheta: (aIdx * Math.PI * 2) / Math.max(1, topA.length),
                        angularSpeed: (aIdx % 2 === 0 ? 1 : -1) * (1.25 + aIdx * 0.28),
                    })
                })
            }

            // 4. 为核心 Hub Cell、L3 Noetic 神经节、Atom/Molecule 粒子、下钻焦点或选中节点生成科幻切角全息投影铭牌
            const isParticle = n.kind === 'atom' || n.kind === 'molecule'
            const showLabel =
                isFocus || n.id === this.selectedNodeId || isParticle || nodes.length <= 48 || topCellIds.has(n.id) || topNoeticIds.has(n.id)
            if (showLabel) {
                const rawLabel = (n.summary || `#${n.id}`).replace(/\s+/g, ' ').trim()
                const shortText = rawLabel.length > 22 ? `${rawLabel.slice(0, 22)}…` : rawLabel
                const accent = isCell
                    ? '#06b6d4'
                    : n.kind === 'atom'
                      ? '#f59e0b'
                      : n.kind === 'molecule'
                        ? '#a855f7'
                        : isNoetic
                          ? '#ec4899'
                          : n.layer === 'background'
                            ? '#3b82f6'
                            : '#10b981'
                const labelSprite = createTextLabelSprite(shortText, accent, isCell, isDark)
                const zLift = isCell ? somaRad + 7 : isNoetic ? 12 : 8
                labelSprite.position.set(startPos.x, startPos.y, startPos.z + zLift)
                this.labelGroup.add(labelSprite)
                labelSpriteRef = labelSprite
                labelZLiftVal = zLift
            }

            // 4.5 登记节点平滑位移插值或新节点生长动画（0.6s），并为新节点/容器伴随触发扩张冲击波光环
            const nKey = this.nodeKeyById.get(n.id) ?? (n.node_key || String(n.id))
            if (isNew || startPos.distanceToSquared(targetPos) > 0.01) {
                this.nodeTransitions.set(n.id, {
                    nodeId: n.id,
                    nodeKey: nKey,
                    startPos: startPos.clone(),
                    targetPos: targetPos.clone(),
                    bornAt: nowElapsed,
                    duration: TRANSITION_DURATION,
                    isNew,
                    spawnScale: initSpawnScale,
                    membraneMesh: membraneMeshRef,
                    plumeSprite: plumeSpriteRef,
                    somaRad,
                    labelSprite: labelSpriteRef,
                    labelZLift: labelZLiftVal,
                })
            }
            if (isNew) {
                this.nodeSurgeUntil.set(n.id, { peak: 1.45, until: nowElapsed + 1.15 })
                const shouldRing = newCount <= 16 || isCell || isNoetic || spawnedShockwaves < 12
                if (shouldRing && spawnedShockwaves < 16) {
                    spawnedShockwaves++
                    this.spawnShockwaveRing(targetPos, nowElapsed, 0.04, 0.78, isCell ? somaRad * 0.35 : 4, isCell ? somaRad * 1.65 : 28)
                }
            }
        }

        // 5. 构建三维有机神经轴突束（3D Curved Neural Axons）与动作电位脉冲流（Action Potential Pulses）
        for (const e of edges) {
            const pA = this.targetPos3DById.get(e.from) ?? this.pos3DById.get(e.from)
            const pB = this.targetPos3DById.get(e.to) ?? this.pos3DById.get(e.to)
            if (!pA || !pB) continue

            const isCites = e.relation === 'cites'
            const isWeak = e.relation === 'weak_edge' || e.relation === 'epistemic_void_snap'
            const isMember = e.relation === 'member_of'

            // 计算三维二次贝塞尔曲线控制点
            const mid = computeAxonControlPoint(pA, pB, e.relation, isSphere)
            const curve = new THREE.QuadraticBezierCurve3(pA.clone(), mid, pB.clone())
            const segs = isMember ? 12 : 28
            const pts = curve.getPoints(segs)
            const lineGeo = new THREE.BufferGeometry().setFromPoints(pts)

            const colHex = isCites ? '#ec4899' : isWeak ? '#f59e0b' : '#06b6d4'
            const baseOpacity = isCites ? 0.34 : isWeak ? 0.36 : 0.18
            const lineMat = new THREE.LineBasicMaterial({
                color: colHex,
                transparent: true,
                opacity: baseOpacity,
                depthWrite: false,
                blending: THREE.AdditiveBlending,
            })
            const line = new THREE.Line(lineGeo, lineMat)
            line.renderOrder = 3
            this.axonGroup.add(line)
            this.axonLines.push({ line, curve, segs, from: e.from, to: e.to, relation: e.relation, baseOpacity })

            // 为 cites（知性引证束）、weak_edge（跨地址突触桥）及下钻焦点边注入实时流动的「动作电位神经脉冲光子」
            if (isCites || isWeak || inDrill || edges.length <= 160) {
                const photonCount = isCites ? 3 : isWeak ? 2 : 1
                const phases: number[] = []
                for (let k = 0; k < photonCount; k++) {
                    phases.push((k / photonCount + (e.id % 17) * 0.059) % 1.0)
                }
                this.pulseStreams.push({
                    curve,
                    relation: e.relation,
                    fromId: e.from,
                    toId: e.to,
                    baseSpeed: isCites ? 0.36 : isWeak ? 0.24 : 0.18,
                    phases,
                    color: new THREE.Color(colHex),
                })
            }

            if (showEdgeLabels && !isMember && edges.length <= 180) {
                const apex = curve.getPoint(0.5)
                const relSprite = createTextLabelSprite(e.relation, colHex, false, isDark)
                relSprite.scale.multiplyScalar(0.82)
                relSprite.position.set(apex.x, apex.y, apex.z + 4)
                this.labelGroup.add(relSprite)
            }
        }

        // 6. 组装电影级「彗星拖尾动作电位脉冲粒子系统（Comet-Tail Action Potential Photons · 5 级连珠拖尾）」
        const COMET_TAIL_STEPS = 5
        let basePhotonHeads = 0
        for (const s of this.pulseStreams) basePhotonHeads += s.phases.length
        const totalPhotons = basePhotonHeads * COMET_TAIL_STEPS
        if (totalPhotons > 0) {
            const pPositions = new Float32Array(totalPhotons * 3)
            const pColors = new Float32Array(totalPhotons * 3)
            const pSizes = new Float32Array(totalPhotons)
            const pAlphas = new Float32Array(totalPhotons)
            let offset = 0
            for (const s of this.pulseStreams) {
                for (const ph of s.phases) {
                    for (let tail = 0; tail < COMET_TAIL_STEPS; tail++) {
                        const tailPhase = (ph - tail * 0.022 + 1.0) % 1.0
                        const pt = s.curve.getPoint(tailPhase)
                        pPositions[offset * 3] = pt.x
                        pPositions[offset * 3 + 1] = pt.y
                        pPositions[offset * 3 + 2] = pt.z
                        pColors[offset * 3] = s.color.r
                        pColors[offset * 3 + 1] = s.color.g
                        pColors[offset * 3 + 2] = s.color.b
                        pSizes[offset] = tail === 0 ? 11.5 : Math.max(3.2, 9.0 - tail * 1.55)
                        pAlphas[offset] = tail === 0 ? 0.98 : Math.max(0.14, 0.72 - tail * 0.145)
                        offset++
                    }
                }
            }
            const pGeo = new THREE.BufferGeometry()
            pGeo.setAttribute('position', new THREE.BufferAttribute(pPositions, 3))
            pGeo.setAttribute('color', new THREE.BufferAttribute(pColors, 3))
            pGeo.setAttribute('aSize', new THREE.BufferAttribute(pSizes, 1))
            pGeo.setAttribute('aAlpha', new THREE.BufferAttribute(pAlphas, 1))
            const pMat = new THREE.ShaderMaterial({
                vertexShader: PULSE_PHOTON_VERTEX_SHADER,
                fragmentShader: PULSE_PHOTON_FRAGMENT_SHADER,
                vertexColors: true,
                transparent: true,
                depthWrite: false,
                blending: THREE.AdditiveBlending,
            })
            this.pulsePoints = new THREE.Points(pGeo, pMat)
            this.pulsePoints.renderOrder = 12
            this.rootGroup.add(this.pulsePoints)
        }

        // 7. 遴选「最近被激活 / 高连接度」种子节点队列（供自动神经放电巡航与首帧激活扩散使用）
        const noeticSeeds = nodes
            .filter((x) => x.kind !== 'cell' && x.layer === 'noetic' && (this.adjacencyMap.get(x.id)?.length ?? 0) > 0)
            .sort((a, b) => (b.observed_at || '').localeCompare(a.observed_at || '') || b.viewDegree - a.viewDegree)
            .slice(0, 8)
            .map((x) => x.id)
        const unitSeeds = nodes
            .filter((x) => x.kind !== 'cell' && x.layer !== 'noetic' && (this.adjacencyMap.get(x.id)?.length ?? 0) > 0)
            .sort((a, b) => (b.observed_at || '').localeCompare(a.observed_at || '') || b.viewDegree - a.viewDegree)
            .slice(0, 6)
            .map((x) => x.id)
        this.recentSeedIds = [...noeticSeeds, ...unitSeeds]
        this.autoPulseCursor = 0

        this.updateHighlightState()
        if (forceFit) {
            this.fitToViewport(false)
        }
        // 首屏加载完成后立即触发一次最近记忆的 1-Hop -> 2-Hop 分形生物闪电扩散，呈现活体宇宙生命感
        if (this.recentSeedIds.length > 0) {
            this.lastAutoPulseAt = this.clock.getElapsedTime()
            this.triggerActivationCascade(this.recentSeedIds[0], false)
        }
    }

    /** 开启或关闭自动生物电放电巡航 */
    public setAutoPulse(enabled: boolean): void {
        this.autoPulseEnabled = enabled
        if (enabled) {
            this.lastAutoPulseAt = this.clock.getElapsedTime() - 3.2
        }
    }

    /** 开启或关闭相机缓慢天体环绕巡航 */
    public setAutoOrbit(enabled: boolean): void {
        this.autoOrbitEnabled = enabled
        this.controls.autoRotate = enabled
    }

    /** 悬停节点瞬间向其 1-Hop 直接邻居发射短促微闪电，强化神经感应反馈 */
    private spawnTransientHoverLightning(nodeId: number): void {
        const pA = this.targetPos3DById.get(nodeId) ?? this.pos3DById.get(nodeId)
        if (!pA) return
        const neighbors = (this.adjacencyMap.get(nodeId) ?? []).slice(0, 6)
        const now = this.clock.getElapsedTime()
        for (const nb of neighbors) {
            const pB = this.targetPos3DById.get(nb.peerId) ?? this.pos3DById.get(nb.peerId)
            if (!pB) continue
            this.createLightningBolt(nodeId, nb.peerId, pA, pB, now, 0, 0.55, 1, nb.relation)
        }
    }

    /**
     * 触发一次「活体记忆激活扩散级联（1-Hop 直接突触闪电 -> 2-Hop 跨地址共振闪电 + 球壳去极化冲击波环）」
     */
    public triggerActivationCascade(seedNodeId?: number, flyTo = false): number | null {
        let targetId = seedNodeId
        if (targetId === undefined || (!this.targetPos3DById.has(targetId) && !this.pos3DById.has(targetId))) {
            if (this.recentSeedIds.length === 0) return null
            targetId = this.recentSeedIds[this.autoPulseCursor % this.recentSeedIds.length]!
            this.autoPulseCursor++
        }
        const seedPos = this.targetPos3DById.get(targetId) ?? this.pos3DById.get(targetId)
        if (!seedPos) return null

        const now = this.clock.getElapsedTime()
        this.nodeSurgeUntil.set(targetId, { peak: 1.55, until: now + 1.85 })
        this.spawnShockwaveRing(seedPos, now, 0, 1.35, 6, 46)

        // 1-Hop：从震源向所有直接关联节点劈出高能分形闪电
        const hop1Neighbors = (this.adjacencyMap.get(targetId) ?? []).slice(0, 10)
        const hop1Set = new Set<number>([targetId])
        for (const nb of hop1Neighbors) {
            hop1Set.add(nb.peerId)
            const pB = this.targetPos3DById.get(nb.peerId) ?? this.pos3DById.get(nb.peerId)
            if (!pB) continue
            this.createLightningBolt(targetId, nb.peerId, seedPos, pB, now, 0, 1.45, 1, nb.relation)
            this.nodeSurgeUntil.set(nb.peerId, { peak: 1.38, until: now + 1.75 })
            const isCell = this.nodeById.get(nb.peerId)?.kind === 'cell'
            const rad = this.cellRadiusById.get(nb.peerId) ?? 18
            this.spawnShockwaveRing(pB, now, 0.14, 1.25, isCell ? rad * 0.55 : 5, isCell ? rad * 1.65 : 32)
        }

        // 2-Hop：延迟 0.28s 沿着 1-Hop 节点的父容器与跨地址突触桥（weak_edge / member_of）触发二级激活扩散
        let hop2Count = 0
        for (const h1Id of hop1Neighbors.map((x) => x.peerId)) {
            if (hop2Count >= 14) break
            const p1 = this.targetPos3DById.get(h1Id) ?? this.pos3DById.get(h1Id)
            if (!p1) continue
            for (const nb2 of this.adjacencyMap.get(h1Id) ?? []) {
                if (hop1Set.has(nb2.peerId)) continue
                if (hop2Count >= 14) break
                const p2 = this.targetPos3DById.get(nb2.peerId) ?? this.pos3DById.get(nb2.peerId)
                if (!p2) continue
                hop1Set.add(nb2.peerId)
                hop2Count++
                this.createLightningBolt(h1Id, nb2.peerId, p1, p2, now, 0.28, 1.4, 2, nb2.relation)
                this.nodeSurgeUntil.set(nb2.peerId, { peak: 1.25, until: now + 1.95 })
                const isCell2 = this.nodeById.get(nb2.peerId)?.kind === 'cell'
                if (isCell2) {
                    const rad2 = this.cellRadiusById.get(nb2.peerId) ?? 24
                    this.spawnShockwaveRing(p2, now, 0.34, 1.35, rad2 * 0.6, rad2 * 1.75)
                }
            }
        }

        if (flyTo) {
            this.flyToNode(targetId, false)
        }
        return targetId
    }

    private createLightningBolt(
        fromId: number,
        toId: number,
        pA: THREE.Vector3,
        pB: THREE.Vector3,
        bornAt: number,
        delay: number,
        duration: number,
        hop: 1 | 2,
        relation: string
    ): void {
        // 控制同时存在的闪电数量上限以保证 60FPS
        while (this.lightningBolts.length > 32) {
            const old = this.lightningBolts.shift()!
            this.fxGroup.remove(old.coreLine, old.glowLine, old.branchLine, old.beadPoints)
            old.coreLine.geometry.dispose()
            old.glowLine.geometry.dispose()
            old.branchLine.geometry.dispose()
            old.beadPoints.geometry.dispose()
        }

        const dist = pA.distanceTo(pB)
        const jitterAmp = Math.min(24, Math.max(5.0, dist * (hop === 1 ? 0.095 : 0.07)))
        const corePts = computeJaggedLightningPoints(pA, pB, jitterAmp, 18)
        const glowPts = computeJaggedLightningPoints(pA, pB, jitterAmp * 1.4, 14)

        // 生成树枝状分叉侧电弧（从主干 35%~65% 处向外劈出）
        const branchStart = corePts[Math.floor(corePts.length * 0.42)] ?? pA
        const branchEnd = new THREE.Vector3()
            .lerpVectors(branchStart, pB, 0.55)
            .add(
                new THREE.Vector3(
                    (Math.random() - 0.5) * jitterAmp * 3.2,
                    (Math.random() - 0.5) * jitterAmp * 3.2,
                    (Math.random() - 0.3) * jitterAmp * 2.2
                )
            )
        const branchPts = computeJaggedLightningPoints(branchStart, branchEnd, jitterAmp * 0.75, 8)

        const isCites = relation === 'cites'
        const isWeak = relation === 'weak_edge' || relation === 'epistemic_void_snap'
        const coreHex = hop === 1 ? (isCites ? '#fdf2f8' : '#ecfeff') : isWeak ? '#fef08a' : '#bae6fd'
        const glowHex = hop === 1 ? (isCites ? '#ec4899' : '#22d3ee') : isWeak ? '#f59e0b' : '#3b82f6'

        const coreGeo = new THREE.BufferGeometry().setFromPoints(corePts)
        const coreMat = new THREE.LineBasicMaterial({
            color: coreHex,
            transparent: true,
            opacity: 0,
            depthWrite: false,
            blending: THREE.AdditiveBlending,
        })
        const coreLine = new THREE.Line(coreGeo, coreMat)
        coreLine.renderOrder = 16

        const glowGeo = new THREE.BufferGeometry().setFromPoints(glowPts)
        const glowMat = new THREE.LineBasicMaterial({
            color: glowHex,
            transparent: true,
            opacity: 0,
            depthWrite: false,
            blending: THREE.AdditiveBlending,
        })
        const glowLine = new THREE.Line(glowGeo, glowMat)
        glowLine.renderOrder = 15

        const branchGeo = new THREE.BufferGeometry().setFromPoints(branchPts)
        const branchMat = new THREE.LineBasicMaterial({
            color: glowHex,
            transparent: true,
            opacity: 0,
            depthWrite: false,
            blending: THREE.AdditiveBlending,
        })
        const branchLine = new THREE.Line(branchGeo, branchMat)
        branchLine.renderOrder = 15

        // 沿电弧拐点布置等离子能量珠串（突破 WebGL 1px 线宽限制，赋予电弧电影级体积辉光）
        const beadGeo = new THREE.BufferGeometry().setFromPoints(corePts.filter((_, idx) => idx % 2 === 1))
        const beadMat = new THREE.PointsMaterial({
            size: hop === 1 ? 11.5 : 8.5,
            map: isCites ? this.glowTextures.noetic : isWeak ? this.glowTextures.focus : this.glowTextures.cell,
            color: coreHex,
            transparent: true,
            opacity: 0,
            depthWrite: false,
            blending: THREE.AdditiveBlending,
        })
        const beadPoints = new THREE.Points(beadGeo, beadMat)
        beadPoints.renderOrder = 17

        this.fxGroup.add(coreLine, glowLine, branchLine, beadPoints)
        this.lightningBolts.push({
            coreLine,
            glowLine,
            branchLine,
            beadPoints,
            fromId,
            toId,
            pA: pA.clone(),
            pB: pB.clone(),
            bornAt,
            delay,
            duration,
            jitterAmp,
            lastJitterAt: bornAt,
            hop,
        })
    }

    private spawnShockwaveRing(center: THREE.Vector3, bornAt: number, delay: number, duration: number, startRadius: number, endRadius: number): void {
        if (!this.shockwaveTexture) return
        while (this.shockwaves.length > 18) {
            const old = this.shockwaves.shift()!
            this.fxGroup.remove(old.mesh)
            old.mesh.geometry.dispose()
        }
        const geo = new THREE.PlaneGeometry(2, 2)
        const mat = new THREE.MeshBasicMaterial({
            map: this.shockwaveTexture,
            transparent: true,
            opacity: 0,
            depthWrite: false,
            side: THREE.DoubleSide,
            blending: THREE.AdditiveBlending,
        })
        const mesh = new THREE.Mesh(geo, mat)
        mesh.position.copy(center)
        mesh.scale.setScalar(startRadius)
        mesh.renderOrder = 14
        this.fxGroup.add(mesh)
        this.shockwaves.push({
            mesh,
            bornAt,
            delay,
            duration,
            startRadius,
            endRadius,
        })
    }

    private updateHighlightState(): void {
        const activeId = this.hoveredNodeId ?? this.selectedNodeId
        const hop1Nodes = new Set<number>()
        const hop2Nodes = new Set<number>()
        if (activeId !== null) {
            hop1Nodes.add(activeId)
            for (const ax of this.axonLines) {
                if (ax.from === activeId) hop1Nodes.add(ax.to)
                if (ax.to === activeId) hop1Nodes.add(ax.from)
            }
            for (const ax of this.axonLines) {
                if (hop1Nodes.has(ax.from) && !hop1Nodes.has(ax.to)) hop2Nodes.add(ax.to)
                if (hop1Nodes.has(ax.to) && !hop1Nodes.has(ax.from)) hop2Nodes.add(ax.from)
            }
        }

        for (const [id, mat] of this.somaMaterials.entries()) {
            if (activeId === null) {
                mat.uniforms.uHighlight!.value = 0.0
            } else if (id === activeId) {
                mat.uniforms.uHighlight!.value = 1.35
            } else if (hop1Nodes.has(id)) {
                mat.uniforms.uHighlight!.value = 0.85
            } else if (hop2Nodes.has(id)) {
                mat.uniforms.uHighlight!.value = 0.35
            } else {
                mat.uniforms.uHighlight!.value = -0.35
            }
        }

        for (const [id, halo] of this.nodeHaloSprites.entries()) {
            const mat = halo.material as THREE.SpriteMaterial
            if (activeId === null) {
                mat.opacity = 0.78
            } else if (id === activeId) {
                mat.opacity = 1.0
            } else if (hop1Nodes.has(id)) {
                mat.opacity = 0.92
            } else if (hop2Nodes.has(id)) {
                mat.opacity = 0.52
            } else {
                mat.opacity = 0.14
            }
        }

        for (const ax of this.axonLines) {
            const mat = ax.line.material as THREE.LineBasicMaterial
            if (activeId === null) {
                mat.opacity = ax.baseOpacity
            } else if (ax.from === activeId || ax.to === activeId) {
                mat.opacity = 0.98
            } else if (hop1Nodes.has(ax.from) && hop1Nodes.has(ax.to)) {
                mat.opacity = 0.65
            } else if ((hop1Nodes.has(ax.from) && hop2Nodes.has(ax.to)) || (hop1Nodes.has(ax.to) && hop2Nodes.has(ax.from))) {
                mat.opacity = 0.48
            } else {
                mat.opacity = ax.baseOpacity * 0.14
            }
        }
    }

    public setSelectedNode(nodeId: number | null, fly = true): void {
        this.selectedNodeId = nodeId
        this.updateHighlightState()
        if (nodeId !== null && fly) {
            this.flyToNode(nodeId, false)
        }
    }

    public flyToNode(nodeId: number, closeUp = false): void {
        const targetPos = this.targetPos3DById.get(nodeId) ?? this.pos3DById.get(nodeId)
        if (!targetPos) return

        const curTarget = this.controls.target.clone()
        const curCam = this.camera.position.clone()
        const offsetDir = curCam.clone().sub(curTarget).normalize()
        const somaRad = this.cellRadiusById.get(nodeId) ?? 18
        const dist = closeUp ? Math.max(120, somaRad * 3.2) : Math.max(210, Math.min(460, curCam.distanceTo(curTarget) * 0.78))
        const endCam = targetPos.clone().addScaledVector(offsetDir, dist)

        this.flyAnim = {
            startPos: curCam,
            endPos: endCam,
            startTarget: curTarget,
            endTarget: targetPos.clone(),
            t0: performance.now(),
            duration: 620,
        }
    }

    public fitToViewport(animate = true): void {
        const posMap = this.targetPos3DById.size > 0 ? this.targetPos3DById : this.pos3DById
        if (posMap.size === 0) return
        const box = new THREE.Box3()
        for (const [id, p] of posMap.entries()) {
            const r = this.cellRadiusById.get(id) ?? 12
            box.expandByPoint(new THREE.Vector3(p.x - r, p.y - r, p.z - r))
            box.expandByPoint(new THREE.Vector3(p.x + r, p.y + r, p.z + r))
        }
        const center = box.getCenter(new THREE.Vector3())
        const size = box.getSize(new THREE.Vector3())
        const fovRad = (this.camera.fov * Math.PI) / 180
        let dist: number
        let endPos: THREE.Vector3
        if (this.currentIsSphere) {
            const maxSpan = Math.max(160, size.x, size.y, size.z)
            dist = Math.min(4800, Math.max(220, (maxSpan / (2 * Math.tan(fovRad / 2))) * 1.89))
            endPos = new THREE.Vector3(center.x, center.y - dist * 0.76, center.z + dist * 0.65)
        } else {
            // 展开星盘模式（Disc 3D）：按正圆星盘有效跨度与 54° 俯视仰角（sin54°≈0.81, cos54°≈0.59）配准视距，
            // 消除倾斜透视造成的纵向拍扁，让展开星盘饱满圆润地呈现在视口中央
            const effectiveSpan = Math.min(920, Math.max(220, (size.x + size.y) * 0.44))
            dist = Math.min(4800, Math.max(240, (effectiveSpan / (2 * Math.tan(fovRad / 2))) * 1.78))
            endPos = new THREE.Vector3(center.x, center.y - dist * 0.59, center.z + dist * 0.81)
        }
        if (!animate) {
            this.controls.target.copy(center)
            this.camera.position.copy(endPos)
            this.controls.update()
            return
        }
        this.flyAnim = {
            startPos: this.camera.position.clone(),
            endPos,
            startTarget: this.controls.target.clone(),
            endTarget: center,
            t0: performance.now(),
            duration: 580,
        }
    }

    public zoomBy(factor: number): void {
        const dir = this.camera.position.clone().sub(this.controls.target)
        const nextLen = Math.max(50, Math.min(4200, dir.length() / factor))
        dir.setLength(nextLen)
        this.camera.position.copy(this.controls.target).add(dir)
        this.controls.update()
    }

    public setZoomEnabled(enabled: boolean): void {
        this.controls.enableZoom = enabled
    }

    public exportPngDataURL(): string {
        this.renderer.render(this.scene, this.camera)
        return this.renderer.domElement.toDataURL('image/png')
    }

    /** 返回当前三维节点云的 (dx, dy, dz) 空间包围盒跨度，用于校验真球形各向同性 vs 扁平星盘 */
    public getBoundingSpan(): { dx: number; dy: number; dz: number } {
        const posMap = this.targetPos3DById.size > 0 ? this.targetPos3DById : this.pos3DById
        if (posMap.size === 0) return { dx: 0, dy: 0, dz: 0 }
        const box = new THREE.Box3()
        for (const p of posMap.values()) {
            box.expandByPoint(p)
        }
        const sz = box.getSize(new THREE.Vector3())
        return {
            dx: Math.round(sz.x),
            dy: Math.round(sz.y),
            dz: Math.round(sz.z),
        }
    }

    public resize(width: number, height: number): void {
        if (width <= 0 || height <= 0) return
        this.camera.aspect = width / height
        this.camera.updateProjectionMatrix()
        this.renderer.setSize(width, height)
    }

    private startAnimationLoop(): void {
        let prevTime = this.clock.getElapsedTime()
        const tick = () => {
            if (this.disposed) return
            this.animFrameId = requestAnimationFrame(tick)

            const elapsed = this.clock.getElapsedTime()
            const dt = Math.min(0.05, elapsed - prevTime)
            prevTime = elapsed

            // 0. 自动生物电放电巡航：每隔 3.8 秒自动沿最近激活的记忆链路劈出一次 1-Hop -> 2-Hop 分形闪电扩散波
            if (this.autoPulseEnabled && this.hoveredNodeId === null && this.recentSeedIds.length > 0 && elapsed - this.lastAutoPulseAt >= 3.8) {
                this.lastAutoPulseAt = elapsed
                this.triggerActivationCascade(undefined, false)
            }

            // 1. 驱动所有 Cell 光晕球壳的 GLSL 呼吸相位与电激增亮
            for (const [id, mat] of this.somaMaterials.entries()) {
                mat.uniforms.uTime!.value = elapsed
                const surge = this.nodeSurgeUntil.get(id)
                if (surge) {
                    if (elapsed >= surge.until) {
                        this.nodeSurgeUntil.delete(id)
                    } else {
                        const rem = Math.max(0, (surge.until - elapsed) / 1.8)
                        mat.uniforms.uHighlight!.value = Math.max(mat.uniforms.uHighlight!.value, surge.peak * rem)
                    }
                }
            }

            // 1.2 驱动存量节点跨刷新平滑位移插值 & 新增节点/容器从父容器或原点 0->1 生长动画（~0.6s）
            let hadActiveTransition = false
            if (this.nodeTransitions.size > 0) {
                hadActiveTransition = true
                for (const [id, tr] of this.nodeTransitions.entries()) {
                    const u = Math.min(1, Math.max(0, (elapsed - tr.bornAt) / tr.duration))
                    const ease = 1 - Math.pow(1 - u, 3)
                    const curPos = new THREE.Vector3().lerpVectors(tr.startPos, tr.targetPos, ease)
                    this.pos3DById.set(id, curPos)
                    this.prevPos3DByKey.set(tr.nodeKey, curPos.clone())
                    tr.spawnScale = tr.isNew ? Math.max(0.001, ease) : 1.0

                    const coreMesh = this.nodeCoreMeshes.get(id)
                    if (coreMesh) coreMesh.position.copy(curPos)
                    const halo = this.nodeHaloSprites.get(id)
                    if (halo) halo.position.copy(curPos)
                    if (tr.membraneMesh) {
                        tr.membraneMesh.position.copy(curPos)
                        tr.membraneMesh.scale.setScalar(tr.somaRad * tr.spawnScale)
                    }
                    if (tr.plumeSprite) {
                        tr.plumeSprite.position.copy(curPos)
                        tr.plumeSprite.scale.setScalar(tr.somaRad * 2.1 * tr.spawnScale)
                    }
                    if (tr.labelSprite) {
                        tr.labelSprite.position.set(curPos.x, curPos.y, curPos.z + tr.labelZLift)
                    }
                    if (u >= 1) {
                        this.nodeTransitions.delete(id)
                    }
                }
            }

            // 1.5 驱动容器内记忆囊泡沿三维开普勒内轨缓慢公转（Intracellular Orbital Circulation）及节点电激搏动
            if (this.intracellularOrbits.length > 0) {
                for (const orb of this.intracellularOrbits) {
                    const cellCenter = this.pos3DById.get(orb.parentId)
                    if (!cellCenter) continue
                    const th = orb.baseTheta + elapsed * orb.angularSpeed
                    const bob = Math.sin(elapsed * 1.6 + orb.bobPhase) * 1.4
                    const ux = cellCenter.x + Math.cos(th) * orb.radiusAtY * orb.radius
                    const uy = cellCenter.y + Math.sin(th) * orb.radiusAtY * orb.radius
                    const uz = cellCenter.z + orb.yRatio * orb.radius + bob
                    const tr = this.nodeTransitions.get(orb.nodeId)
                    if (tr) {
                        tr.targetPos.set(ux, uy, uz)
                        continue
                    }
                    const curPos = this.pos3DById.get(orb.nodeId)
                    if (curPos) curPos.set(ux, uy, uz)
                    const nKey = this.nodeKeyById.get(orb.nodeId)
                    if (nKey) {
                        const prev = this.prevPos3DByKey.get(nKey)
                        if (prev) prev.set(ux, uy, uz)
                        else this.prevPos3DByKey.set(nKey, new THREE.Vector3(ux, uy, uz))
                    }
                    const mesh = this.nodeCoreMeshes.get(orb.nodeId)
                    if (mesh) mesh.position.set(ux, uy, uz)
                    const halo = this.nodeHaloSprites.get(orb.nodeId)
                    if (halo) halo.position.set(ux, uy, uz)
                }
            }

            // 1.6 驱动量子原子微粒（Atomic Quantum Satellites）沿三维倾斜开普勒轨道高速公转
            if (this.atomicSatellites.length > 0) {
                for (const sat of this.atomicSatellites) {
                    const hostPos = this.pos3DById.get(sat.hostNodeId)
                    if (!hostPos) continue
                    const th = sat.baseTheta + elapsed * sat.angularSpeed
                    const c = Math.cos(th)
                    const s = Math.sin(th)
                    const sx = hostPos.x + sat.radius * c
                    const sy = hostPos.y + sat.radius * s * Math.cos(sat.tiltX)
                    const sz = hostPos.z + sat.radius * (s * Math.sin(sat.tiltX) + c * Math.sin(sat.tiltY) * 0.45)
                    sat.sprite.position.set(sx, sy, sz)
                }
            }

            // 节点被闪电击中时的电激搏动放大（Surge Pulsation）与新节点生长缩放合成
            for (const [id, mesh] of this.nodeCoreMeshes.entries()) {
                const baseS = this.nodeBaseScale.get(id) ?? 4.8
                const halo = this.nodeHaloSprites.get(id)
                const baseH = this.haloBaseScale.get(id) ?? 22
                const surge = this.nodeSurgeUntil.get(id)
                const spawnScale = this.nodeTransitions.get(id)?.spawnScale ?? 1.0
                let mult = 1.0
                if (surge && elapsed < surge.until) {
                    const rem = Math.max(0, (surge.until - elapsed) / 1.8)
                    mult = 1.0 + (surge.peak - 1.0) * rem * (0.85 + 0.15 * Math.sin(elapsed * 22))
                } else if (surge) {
                    this.nodeSurgeUntil.delete(id)
                }
                mesh.scale.setScalar(baseS * mult * spawnScale)
                if (halo) halo.scale.setScalar(baseH * (1.0 + (mult - 1.0) * 1.35) * spawnScale)
            }

            // 1.8 在节点平滑位移或新节点生长期间，实时同步神经轴突贝塞尔曲线几何体
            if (hadActiveTransition && this.axonLines.length > 0) {
                for (const ax of this.axonLines) {
                    const pA = this.pos3DById.get(ax.from)
                    const pB = this.pos3DById.get(ax.to)
                    if (!pA || !pB) continue
                    ax.curve.v0.copy(pA)
                    ax.curve.v2.copy(pB)
                    computeAxonControlPoint(pA, pB, ax.relation, this.currentIsSphere, ax.curve.v1)
                    ax.line.geometry.setFromPoints(ax.curve.getPoints(ax.segs))
                }
            }

            // 2. 驱动神经轴突上的彗星拖尾动作电位脉冲光子（5 级连珠彗尾）沿三维曲线连续传导
            if (this.pulsePoints && this.pulseStreams.length > 0) {
                const posAttr = this.pulsePoints.geometry.getAttribute('position') as THREE.BufferAttribute
                const activeId = this.hoveredNodeId ?? this.selectedNodeId
                let offset = 0
                for (const s of this.pulseStreams) {
                    const isExcited =
                        (activeId !== null && (s.fromId === activeId || s.toId === activeId)) ||
                        this.nodeSurgeUntil.has(s.fromId) ||
                        this.nodeSurgeUntil.has(s.toId)
                    const speed = s.baseSpeed * (isExcited ? 2.6 : 1.0)
                    for (let k = 0; k < s.phases.length; k++) {
                        s.phases[k] = (s.phases[k]! + dt * speed) % 1.0
                        const headPhase = s.phases[k]!
                        for (let tail = 0; tail < 5; tail++) {
                            const tailPhase = (headPhase - tail * (isExcited ? 0.028 : 0.02) + 1.0) % 1.0
                            const pt = s.curve.getPoint(tailPhase)
                            posAttr.setXYZ(offset, pt.x, pt.y, pt.z)
                            offset++
                        }
                    }
                }
                posAttr.needsUpdate = true
            }

            // 2.5 驱动分形生物闪电电弧（主电弧 + 树状分叉 + 等离子电晕珠串：高频重分叉抖动 + 余晖衰减）
            for (let i = this.lightningBolts.length - 1; i >= 0; i--) {
                const b = this.lightningBolts[i]!
                const localT = elapsed - (b.bornAt + b.delay)
                const coreMat = b.coreLine.material as THREE.LineBasicMaterial
                const glowMat = b.glowLine.material as THREE.LineBasicMaterial
                const branchMat = b.branchLine.material as THREE.LineBasicMaterial
                const beadMat = b.beadPoints.material as THREE.PointsMaterial
                if (localT < 0) {
                    coreMat.opacity = 0
                    glowMat.opacity = 0
                    branchMat.opacity = 0
                    beadMat.opacity = 0
                    continue
                }
                if (localT >= b.duration) {
                    this.fxGroup.remove(b.coreLine, b.glowLine, b.branchLine, b.beadPoints)
                    b.coreLine.geometry.dispose()
                    b.glowLine.geometry.dispose()
                    b.branchLine.geometry.dispose()
                    b.beadPoints.geometry.dispose()
                    this.lightningBolts.splice(i, 1)
                    continue
                }
                const progress = localT / b.duration
                // 快速起弧、高频闪烁、指数余晖衰减
                const flicker = 0.72 + 0.28 * Math.sin(elapsed * 52 + i * 3.1)
                const fade = Math.pow(1 - progress, 1.4) * flicker
                coreMat.opacity = Math.min(1.0, fade * (b.hop === 1 ? 1.0 : 0.88))
                glowMat.opacity = Math.min(0.92, fade * (b.hop === 1 ? 0.75 : 0.58))
                branchMat.opacity = Math.min(0.85, fade * 0.65)
                beadMat.opacity = Math.min(0.96, fade * (b.hop === 1 ? 0.92 : 0.72))

                // 每 55ms 重新生成一次锯齿顶点与等离子珠位置，呈现真实的活体生物电弧跳跃感
                if (elapsed - b.lastJitterAt >= 0.055) {
                    b.lastJitterAt = elapsed
                    const curA = this.pos3DById.get(b.fromId) ?? b.pA
                    const curB = this.pos3DById.get(b.toId) ?? b.pB
                    const nextCorePts = computeJaggedLightningPoints(curA, curB, b.jitterAmp, 18)
                    b.coreLine.geometry.setFromPoints(nextCorePts)
                    b.glowLine.geometry.setFromPoints(computeJaggedLightningPoints(curA, curB, b.jitterAmp * 1.4, 14))
                    b.beadPoints.geometry.setFromPoints(nextCorePts.filter((_, idx) => idx % 2 === 1))
                    const brStart = nextCorePts[Math.floor(nextCorePts.length * 0.42)] ?? curA
                    const brEnd = new THREE.Vector3()
                        .lerpVectors(brStart, curB, 0.52)
                        .add(
                            new THREE.Vector3(
                                (Math.random() - 0.5) * b.jitterAmp * 3.0,
                                (Math.random() - 0.5) * b.jitterAmp * 3.0,
                                (Math.random() - 0.3) * b.jitterAmp * 2.0
                            )
                        )
                    b.branchLine.geometry.setFromPoints(computeJaggedLightningPoints(brStart, brEnd, b.jitterAmp * 0.75, 8))
                }
            }

            // 2.8 驱动容器去极化扩张冲击波环（Expanding Depolarization Shockwave Rings，始终朝向相机）
            for (let i = this.shockwaves.length - 1; i >= 0; i--) {
                const sw = this.shockwaves[i]!
                const localT = elapsed - (sw.bornAt + sw.delay)
                const mat = sw.mesh.material as THREE.MeshBasicMaterial
                if (localT < 0) {
                    mat.opacity = 0
                    continue
                }
                if (localT >= sw.duration) {
                    this.fxGroup.remove(sw.mesh)
                    sw.mesh.geometry.dispose()
                    this.shockwaves.splice(i, 1)
                    continue
                }
                const u = localT / sw.duration
                const easeOut = 1 - Math.pow(1 - u, 2.4)
                const curR = sw.startRadius + (sw.endRadius - sw.startRadius) * easeOut
                sw.mesh.scale.setScalar(curR)
                sw.mesh.quaternion.copy(this.camera.quaternion)
                mat.opacity = Math.pow(1 - u, 1.65) * 0.72
            }

            // 3. 深空十字星芒闪烁粒子、旋臂星尘流与体积星云云雾缓慢旋转
            if (this.ambientStars) {
                this.ambientStars.rotation.z = elapsed * 0.011
                const starMat = this.ambientStars.material as THREE.ShaderMaterial
                if (starMat.uniforms?.uTime) {
                    starMat.uniforms.uTime.value = elapsed
                }
            }
            for (const ch of this.somaGroup.children) {
                if ((ch as THREE.Points).isPoints) {
                    ch.rotation.z = elapsed * 0.028
                    const pm = (ch as THREE.Points).material as THREE.ShaderMaterial
                    if (pm.uniforms?.uTime) {
                        pm.uniforms.uTime.value = elapsed
                    }
                }
            }
            this.nebulaCloudGroup.rotation.z = -elapsed * 0.005

            // 悬停节点或正在相机飞行时暂停自动环绕，离开后恢复平滑天体巡航
            this.controls.autoRotate = this.autoOrbitEnabled && this.hoveredNodeId === null && this.flyAnim === null

            // 4. 平滑相机飞行插值
            if (this.flyAnim) {
                const u = Math.min(1, (performance.now() - this.flyAnim.t0) / this.flyAnim.duration)
                const ease = 1 - Math.pow(1 - u, 3)
                this.camera.position.lerpVectors(this.flyAnim.startPos, this.flyAnim.endPos, ease)
                this.controls.target.lerpVectors(this.flyAnim.startTarget, this.flyAnim.endTarget, ease)
                if (u >= 1) {
                    this.flyAnim = null
                }
            }

            this.controls.update()
            this.renderer.render(this.scene, this.camera)
        }
        this.animFrameId = requestAnimationFrame(tick)
    }

    public dispose(): void {
        this.disposed = true
        if (this.animFrameId) cancelAnimationFrame(this.animFrameId)
        if (this.clickDelayTimer) clearTimeout(this.clickDelayTimer)
        this.controls.dispose()
        this.renderer.dispose()
        for (const tex of Object.values(this.glowTextures)) {
            tex.dispose()
        }
        this.shockwaveTexture?.dispose()
    }
}
