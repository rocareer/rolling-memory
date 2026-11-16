/// <reference types="vite/client" />

declare module '*.vue' {
    import { DefineComponent } from 'vue'
    // eslint-disable-next-line @typescript-eslint/no-empty-object-type
    const component: DefineComponent<{}, {}, any>
    export default component
}

// 构建期注入的发布版本串（单一真源 = 仓根 VERSION 文件，见 vite.config.ts define）。
declare const __RM_VERSION__: string
