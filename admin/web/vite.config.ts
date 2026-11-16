import vue from '@vitejs/plugin-vue'
import { existsSync, readFileSync, statSync } from 'fs'
import { resolve } from 'path'
import type { ConfigEnv, Plugin, UserConfig } from 'vite'
import { loadEnv } from 'vite'
import { lucideIconSplitPlugin } from './src/components/icon/vitePlugin.ts'
import { customHotUpdate } from './src/utils/vite.ts'

const webRoot = import.meta.dirname
const srcRoot = resolve(webRoot, 'src')
const brandExtensions = ['.ts', '.js', '.mjs', '.vue', '.scss', '.css', '.json']

/**
 * 品牌 overlay 解析器（构建期品牌层）
 * VITE_BRAND 指定 plugins/<名>/brand 时，src 下的模块按相对路径优先命中品牌包同路径文件，
 * 未命中回退 web/src；品牌包不覆盖核心文件，只做覆盖层（入口 /src/main.ts 同样走本解析器）。
 */
function brandOverlay(VITE_BRAND: string): Plugin {
    const brandRoot = VITE_BRAND ? resolve(webRoot, `../plugins/${VITE_BRAND}/brand`) : ''

    const resolveBrandFile = (relative: string): string | undefined =>
        [relative, ...brandExtensions.map((ext) => relative + ext)]
            .map((item) => resolve(brandRoot, item))
            .find((item) => existsSync(item) && statSync(item).isFile())

    return {
        name: 'rolling-brand-overlay',
        enforce: 'pre',
        async resolveId(source, importer) {
            if (!brandRoot || !existsSync(brandRoot)) return

            // 品牌件里的裸模块导入（element-plus / vue / mitt …）按 web 根解析：
            // 品牌件位于 plugins/**，Node 从那里向上找不到 web/node_modules
            if (importer?.startsWith(`${brandRoot}/`) && !source.startsWith('.') && !source.startsWith('/') && !source.startsWith('@/')) {
                const resolved = await this.resolve(source, resolve(webRoot, 'index.html'), { skipSelf: true })
                if (resolved) return resolved
            }

            const id = source.split('?')[0]
            let relative = ''
            if (id.startsWith('@/')) {
                relative = id.slice(2)
            } else if (id.startsWith('/src/')) {
                relative = id.slice(5)
            } else if (id.startsWith(`${srcRoot}/`)) {
                relative = id.slice(srcRoot.length + 1)
            }

            return relative ? resolveBrandFile(relative) : undefined
        },
    }
}

// https://vitejs.cn/config/
const viteConfig = ({ mode }: ConfigEnv): UserConfig => {
    const { VITE_PORT, VITE_OPEN, VITE_BASE_PATH, VITE_OUT_DIR, VITE_BRAND } = loadEnv(mode, process.cwd())
    const brandRoot = VITE_BRAND ? resolve(webRoot, `../plugins/${VITE_BRAND}/brand`) : ''
    // 前端版本串单一真源 = 仓根 VERSION 文件（与 CLI/Admin 后端同源），构建期经
    // __RM_VERSION__ 注入；.vue/.ts 禁再硬编码版本字面量（发版升号曾致多处漂移假红）。
    const repoVersion = readFileSync(resolve(webRoot, '../../VERSION'), 'utf-8').trim()

    return {
        plugins: [vue(), lucideIconSplitPlugin(), customHotUpdate(), brandOverlay(VITE_BRAND)],
        root: process.cwd(),
        define: {
            __RM_VERSION__: JSON.stringify(repoVersion),
        },
        resolve: {
            alias: {
                '@': resolve(import.meta.dirname, 'src'),
            },
        },
        base: VITE_BASE_PATH,
        server: {
            host: '0.0.0.0',
            port: parseInt(VITE_PORT),
            strictPort: true,
            open: VITE_OPEN != 'false',
            fs: {
                // 品牌 overlay 位于 web 根之外（plugins/<名>/brand），dev 需显式放行
                allow: [webRoot, ...(brandRoot ? [brandRoot] : [])],
            },
        },
        build: {
            cssCodeSplit: false,
            sourcemap: false,
            outDir: VITE_OUT_DIR,
            emptyOutDir: true,
            chunkSizeWarningLimit: 1500,
        },
    }
}

export default viteConfig
