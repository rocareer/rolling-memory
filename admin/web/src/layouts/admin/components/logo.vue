<template>
    <div
        class="layout-title-bar"
        :class="[config.layout.menuTopBarCenter ? 'layout-title-bar-center' : '', config.layout.menuCollapse ? 'collapse' : '']"
    >
        <img :src="config.layout.dark ? logoYin : logoYang" class="logo-img" alt="Rolling Memory" />
        <div v-if="!config.layout.menuCollapse" class="website-name">{{ config.site.name || 'Rolling Memory' }}</div>
    </div>
</template>

<script setup lang="ts">
import logoYang from '@/assets/logo-yang.svg'
import logoYin from '@/assets/logo-yin.svg'
import { useConfig } from '@/stores/config'

const config = useConfig()
</script>

<style scoped lang="scss">
.layout-title-bar {
    width: 100%;
    display: flex;
    align-items: center;
    box-sizing: border-box;
    padding: 10px 20px;
    height: v-bind('config.layout.mode == "Double" ?  "60px":"46px"');
    background: v-bind('config.layout.mode == "Streamline" ?  "transparent":config.getColorValue("menuTopBarBackground")');
}
.layout-title-bar-center {
    justify-content: center;
}
.logo-img {
    width: 26px;
}
.collapse {
    &.layout-title-bar {
        padding: 10px 0;
    }
    .logo-img {
        margin: 0 auto;
    }
}
.website-name {
    display: block;
    padding: 0 2px;
    font-family: var(--rm-brand-font);
    font-size: var(--el-font-size-extra-large);
    font-weight: 800;
    letter-spacing: -0.02em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: v-bind('config.getColorValue("menuTopBarColor")');
}
.logo-img + .website-name {
    margin-left: 6px;
}
</style>
