<template>
    <div class="login-page" :class="{ 'is-dark': isDark }">
        <aside class="brand-panel">
            <div class="brand-aurora" aria-hidden="true">
                <span class="aurora aurora-brand" />
                <span class="aurora aurora-neutral" />
            </div>

            <div class="brand-main">
                <div class="brand-emblem">
                    <div class="brand-logo">
                        <img :src="isDark ? logoYang : logoYin" class="brand-logo-img" alt="Rolling" />
                        <span class="brand-logo-sweep" aria-hidden="true"><i /></span>
                    </div>
                    <div class="brand-identity">
                        <span class="brand-system-name">{{ t('plugin.rolling-brand.brandSystem') }}</span>
                        <span class="brand-system-triad">{{ t('plugin.rolling-brand.brandTriad') }}</span>
                    </div>
                </div>

                <p class="brand-title">{{ t('plugin.rolling-brand.heroTitle') }}</p>
                <p class="brand-desc">{{ t('plugin.rolling-brand.heroDescription') }}</p>

                <ul class="brand-points">
                    <li v-for="point in points" :key="point.title" class="point">
                        <span class="point-icon-wrap">
                            <Icon :name="point.icon" :size="18" :stroke-width="1.6" class="point-icon" />
                        </span>
                        <span class="point-body">
                            <span class="point-title">{{ t(`plugin.rolling-brand.${point.title}`) }}</span>
                            <span class="point-desc">{{ t(`plugin.rolling-brand.${point.desc}`) }}</span>
                        </span>
                    </li>
                </ul>
            </div>

            <footer class="brand-footer">
                <span>&copy; {{ new Date().getFullYear() }} Rolling · Autonoetic Memory Engine</span>
                <a v-if="config.site.recordNumber" href="http://beian.miit.gov.cn/" target="_blank" rel="noopener">
                    {{ config.site.recordNumber }}
                </a>
                <span class="brand-version">v0.0.4 · Core v1.0.1</span>
            </footer>
        </aside>

        <main class="form-panel">
            <div class="login-toolbar">
                <button type="button" class="toolbar-pill" @click="goHome()">
                    <Icon name="lucide-house" :size="15" />
                    <span>{{ t('login.backHome') }}</span>
                </button>

                <button type="button" class="toolbar-pill" :title="isDark ? t('login.themeLight') : t('login.themeDark')" @click="toggleDark()">
                    <Icon :name="isDark ? 'lucide-sun' : 'lucide-moon'" :size="15" />
                    <span>{{ isDark ? t('login.themeLight') : t('login.themeDark') }}</span>
                </button>

                <el-dropdown trigger="click" @command="(val: any) => setLang(val)">
                    <button type="button" class="toolbar-pill">
                        <Icon name="lucide-languages" :size="15" />
                        <span>{{ langNames[config.lang.active] || '简体中文' }}</span>
                    </button>
                    <template #dropdown>
                        <el-dropdown-menu>
                            <el-dropdown-item v-for="(label, key) in langNames" :key="key" :command="key">
                                {{ label }}
                            </el-dropdown-item>
                        </el-dropdown-menu>
                    </template>
                </el-dropdown>
            </div>

            <div class="form-wrapper">
                <div class="form-card">
                    <div class="form-brand">
                        <span class="form-brand-logo">
                            <img :src="isDark ? logoYang : logoYin" alt="Rolling" />
                        </span>
                        <div class="form-brand-meta">
                            <span class="form-brand-name">Rolling</span>
                            <span class="form-brand-sub">Autonoetic Memory Engine</span>
                        </div>
                    </div>

                    <header class="form-header">
                        <h1>{{ t('login.login') }}</h1>
                        <p>{{ t('login.welcomePrompt', { siteName: config.site.name || 'Rolling' }) }}</p>
                    </header>

                    <el-form
                        ref="formRef"
                        :model="loginForm"
                        :rules="rules"
                        :validate-on-rule-change="false"
                        @submit.prevent=""
                        @keyup.enter="onSubmit"
                    >
                        <el-form-item prop="username">
                            <el-input
                                ref="usernameInputRef"
                                v-model="loginForm.username"
                                :placeholder="t('login.username')"
                                :prefix-icon="User"
                                clearable
                                autocomplete="username"
                            />
                        </el-form-item>

                        <el-form-item prop="password">
                            <el-input
                                ref="passwordInputRef"
                                v-model="loginForm.password"
                                type="password"
                                show-password
                                :placeholder="t('login.password')"
                                :prefix-icon="Lock"
                                clearable
                                autocomplete="current-password"
                            />
                        </el-form-item>

                        <div class="form-options">
                            <el-checkbox v-model="loginForm.remember" :label="t('login.remember')" />
                        </div>

                        <div v-if="loginPreset" class="preset-bar">
                            <Icon name="lucide-sparkles" :size="15" class="preset-icon" />
                            <span class="preset-tip">
                                {{ t('plugin.rolling-brand.presetTip', { username: loginPreset.username }) }}
                            </span>
                            <button type="button" class="preset-fill" @click="fillPreset">
                                {{ t('plugin.rolling-brand.presetFill') }}
                            </button>
                        </div>

                        <el-button v-blur class="submit-btn" :loading="submitLoading" @click="onSubmit">
                            {{ submitLoading ? t('login.loggingIn') : t('login.login') }}
                        </el-button>
                    </el-form>
                </div>

                <footer class="form-footer">
                    <span>&copy; {{ new Date().getFullYear() }} Rolling · Autonoetic Memory Engine</span>
                    <a v-if="config.site.recordNumber" href="http://beian.miit.gov.cn/" target="_blank" rel="noopener">
                        {{ config.site.recordNumber }}
                    </a>
                    <span>v0.0.4 · Core v1.0.1</span>
                </footer>
            </div>
        </main>
    </div>
</template>

<script setup lang="ts">
import { getLoginConfig, login } from '@/api/admin'
import { getSiteConfig } from '@/api/common'
import logoYang from '@/assets/logo-yang.svg'
import logoYin from '@/assets/logo-yin.svg'
import clickCaptcha from '@/components/clickCaptcha'
import type { ClickRequest } from '@/components/clickCaptcha/index'
import { useDark } from '@/hooks/useDark'
import { langNames, setLang } from '@/lang'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { useAdminInfo } from '@/stores/adminInfo'
import { useConfig } from '@/stores/config'
import { getBaseURL } from '@/utils/request'
import { normalizeAdminRedirectPath } from '@/utils/router'
import { Lock, User } from '@element-plus/icons-vue'
import axios from 'axios'
import type { FormInstance, FormRules, InputInstance } from 'element-plus'
import { ElMessage } from 'element-plus'
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const config = useConfig()
const adminInfo = useAdminInfo()
const { isDark, toggleDark } = useDark()

interface LoginForm {
    username: string
    password: string
    remember: boolean
}

const loginForm = reactive<LoginForm>({
    username: '',
    password: '',
    remember: true,
})

const formRef = ref<FormInstance>()
const submitLoading = ref(false)
const captchaEnabled = ref(false)
const usernameInputRef = ref<InputInstance>()
const passwordInputRef = ref<InputInstance>()
const submitting = ref(false)

// 开箱默认账号：由品牌包公开端点下发，且只对来源为本机/内网的访问可见（取不到则整条提示不出现）
interface LoginPreset {
    enabled: boolean
    username: string
    password: string
}

const loginPreset = ref<LoginPreset | null>(null)

const fillPreset = () => {
    if (!loginPreset.value) return

    loginForm.username = loginPreset.value.username
    loginForm.password = loginPreset.value.password
}

const points = [
    { icon: 'lucide-zap', title: 'pointRouterTitle', desc: 'pointRouterDesc' },
    { icon: 'lucide-shield-check', title: 'pointEvalTitle', desc: 'pointEvalDesc' },
    { icon: 'lucide-trending-down', title: 'pointAuditTitle', desc: 'pointAuditDesc' },
]

const rules = computed<FormRules<LoginForm>>(() => ({
    username: [{ required: true, message: t('login.usernameRequired'), trigger: 'change' }],
    password: [
        { required: true, message: t('login.passwordRequired'), trigger: 'change' },
        { min: 6, message: t('login.passwordMinLength'), trigger: 'change' },
    ],
}))

let captchaDestroy: (() => void) | null = null

const destroyCaptcha = () => {
    captchaDestroy?.()
    captchaDestroy = null
}

function goPostLogin() {
    const redirect = route.query.redirect
    const target =
        typeof redirect === 'string' && (redirect === adminBaseRoutePath || redirect.startsWith(`${adminBaseRoutePath}/`))
            ? normalizeAdminRedirectPath(redirect)
            : { name: 'admin' }
    router.replace(target)
}

const goHome = () => {
    router.push('/')
}

const doLogin = (captcha?: ClickRequest) => {
    destroyCaptcha()
    submitLoading.value = true
    login({ ...loginForm, captcha })
        .then((res) => {
            adminInfo.dataFill(res.data.data, false)
            goPostLogin()
        })
        .finally(() => {
            submitLoading.value = false
            submitting.value = false
        })
}

async function onSubmit() {
    if (submitLoading.value || submitting.value) return
    if (!formRef.value) return

    try {
        await formRef.value.validate()
    } catch {
        return
    }

    submitting.value = true
    if (captchaEnabled.value) {
        clickCaptcha((data) => doLogin(data))
        submitting.value = false
    } else {
        doLogin()
    }
}

onMounted(() => {
    getLoginConfig()
        .then((res) => {
            if (res.data.data.type == 'logged_in') {
                ElMessage.success(t('login.loggedIn'))
                goPostLogin()
                return
            }
            captchaEnabled.value = Boolean(res.data.data.captcha)
        })
        .catch(() => {
            captchaEnabled.value = false
        })

    // 品牌包公开端点，非后台接口：直连取值，失败静默（未装品牌包/公网来源时本就没有开箱提示）
    axios
        .get<LoginPreset>(`${getBaseURL()}/plugin/rolling-brand/login-preset.json`, { timeout: 5000 })
        .then((res) => {
            if (res.data?.enabled) loginPreset.value = res.data
        })
        .catch(() => {})
})

onBeforeUnmount(() => {
    destroyCaptcha()
})
</script>

<style scoped lang="scss">
.login-page {
    --zc-bg: #f6f7f9;
    --zc-panel: #efeff3;
    --zc-card: #ffffff;
    --zc-input-bg: #f9f9fb;
    --zc-border: rgba(15, 16, 20, 0.11);
    --zc-border-hover: rgba(15, 16, 20, 0.24);
    --zc-title: #0f1014;
    --zc-foreground: #272830;
    --zc-foreground-subtle: rgba(39, 40, 48, 0.68);
    --zc-foreground-subtlest: rgba(39, 40, 48, 0.45);
    --zc-primary: #0f1014;
    --zc-primary-foreground: #f5f5f7;
    --zc-primary-hover: #262832;
    --zc-brand: #38bdf8;
    --zc-brand-soft: rgba(56, 189, 248, 0.18);
    --zc-danger: #dc2626;
    --zc-danger-soft: rgba(220, 38, 38, 0.16);
    --zc-ring: 0 0 0 1.5px var(--zc-brand) inset, 0 0 0 4px var(--zc-brand-soft);
    --zc-tile-bg: linear-gradient(180deg, #0d0e12 0%, #191b22 100%);
    --zc-tile-border: rgba(255, 255, 255, 0.12);

    display: grid;
    grid-template-columns: 1.08fr 0.92fr;
    min-height: 100dvh;
    color: var(--zc-foreground);
    background: var(--zc-bg);
    transition:
        background-color 0.2s ease,
        color 0.2s ease;
}

.login-page.is-dark,
html.dark .login-page {
    --zc-bg: #090a0d;
    --zc-panel: #0e1015;
    --zc-card: #14161d;
    --zc-input-bg: #0f1117;
    --zc-border: rgba(255, 255, 255, 0.11);
    --zc-border-hover: rgba(255, 255, 255, 0.24);
    --zc-title: #f5f5f7;
    --zc-foreground: #e6e7ee;
    --zc-foreground-subtle: rgba(230, 231, 238, 0.66);
    --zc-foreground-subtlest: rgba(230, 231, 238, 0.42);
    --zc-primary: #f5f5f7;
    --zc-primary-foreground: #0f1014;
    --zc-primary-hover: #e2e3ea;
    --zc-danger: #f87171;
    --zc-danger-soft: rgba(248, 113, 113, 0.18);
    --zc-tile-bg: linear-gradient(180deg, #f8f8fa 0%, #e9eaf0 100%);
    --zc-tile-border: rgba(15, 16, 20, 0.12);
}

.brand-panel {
    position: relative;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    gap: 28px;
    overflow: hidden;
    padding: clamp(32px, 4vw, 60px) clamp(36px, 4.5vw, 68px);
    background: var(--zc-panel);
    border-right: 1px solid var(--zc-border);
}

.brand-aurora {
    position: absolute;
    z-index: 0;
    inset: 0;
    overflow: hidden;
    pointer-events: none;

    .aurora {
        position: absolute;
        display: block;
        border-radius: 50%;
        filter: blur(88px);
    }

    .aurora-brand {
        top: -170px;
        right: -140px;
        width: 540px;
        height: 500px;
        background: rgba(56, 189, 248, 0.16);
        animation: aurora-drift-a 26s ease-in-out infinite;
    }

    .aurora-neutral {
        bottom: -240px;
        left: -180px;
        width: 600px;
        height: 580px;
        background: rgba(168, 85, 247, 0.08);
        animation: aurora-drift-b 34s ease-in-out infinite;
    }
}

.brand-main {
    position: relative;
    z-index: 1;
    max-width: 580px;
    margin: auto 0;
}

.brand-emblem {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 16px;
}

.brand-logo {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 88px;
    height: 88px;
    border-radius: 22px;
    background: var(--zc-tile-bg);
    box-shadow:
        0 18px 28px -6px rgba(0, 0, 0, 0.22),
        0 6px 12px -4px rgba(0, 0, 0, 0.16);

    &::before {
        position: absolute;
        content: '';
        inset: 0;
        border: 1px solid var(--zc-tile-border);
        border-radius: inherit;
        pointer-events: none;
    }

    .brand-logo-img {
        width: 54px;
        height: 54px;
        object-fit: contain;
    }
}

.brand-identity {
    display: flex;
    flex-direction: column;
    gap: 4px;
}

.brand-system-name {
    font-family: var(--rm-brand-font);
    font-size: 21px;
    font-weight: 800;
    letter-spacing: -0.025em;
    color: var(--zc-title);
    line-height: 1.2;
}

.brand-system-triad {
    font-family: var(--rm-brand-font);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.14em;
    color: var(--zc-foreground-subtlest);
}

.brand-logo-sweep {
    position: absolute;
    padding: 1px;
    overflow: hidden;
    pointer-events: none;
    border-radius: inherit;
    inset: 0;
    mask-image: linear-gradient(#fff 0 0), linear-gradient(#fff 0 0);
    mask-clip: content-box, border-box;
    mask-composite: exclude;
    -webkit-mask-image: linear-gradient(#fff 0 0), linear-gradient(#fff 0 0);
    -webkit-mask-clip: content-box, border-box;
    -webkit-mask-composite: xor;

    > i {
        position: absolute;
        display: block;
        background: conic-gradient(
            rgba(0, 0, 0, 0) 0deg 240deg,
            rgba(56, 189, 248, 0.15) 280deg,
            rgba(191, 90, 242, 0.65) 350deg,
            rgba(0, 0, 0, 0) 360deg
        );
        animation: brand-logo-sweep 6s linear infinite;
        inset: -50%;
    }
}

@keyframes brand-logo-sweep {
    to {
        transform: rotate(360deg);
    }
}

@keyframes aurora-drift-a {
    0%,
    100% {
        transform: translate3d(0, 0, 0) scale(1);
    }
    50% {
        transform: translate3d(-36px, 28px, 0) scale(1.06);
    }
}

@keyframes aurora-drift-b {
    0%,
    100% {
        transform: translate3d(0, 0, 0) scale(1);
    }
    50% {
        transform: translate3d(32px, -24px, 0) scale(1.05);
    }
}

.brand-title {
    margin: 26px 0 0;
    font-size: clamp(24px, 2.3vw, 32px);
    font-weight: 700;
    line-height: 1.18;
    color: var(--zc-title);
    letter-spacing: -0.03em;
    white-space: nowrap;
}

.brand-desc {
    margin: 12px 0 0;
    font-size: 13.5px;
    line-height: 1.6;
    color: var(--zc-foreground-subtle);
    white-space: nowrap;
}

.brand-points {
    display: flex;
    flex-direction: column;
    gap: 10px;
    width: 100%;
    max-width: 540px;
    padding: 0;
    margin: 26px 0 0;
    list-style: none;

    .point {
        display: flex;
        gap: 13px;
        align-items: flex-start;
        padding: 13px 16px;
        background: var(--zc-card);
        border: 1px solid var(--zc-border);
        border-radius: 13px;
        box-shadow: 0 2px 6px rgba(0, 0, 0, 0.04);
    }

    .point-icon-wrap {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 32px;
        height: 32px;
        border-radius: 9px;
        background: var(--zc-input-bg);
        border: 1px solid var(--zc-border);
        flex-shrink: 0;
        margin-top: 1px;
    }

    .point-icon {
        color: var(--zc-title);
    }

    .point-body {
        min-width: 0;
    }

    .point-title {
        display: block;
        font-size: 14px;
        font-weight: 600;
        color: var(--zc-title);
        white-space: nowrap;
    }

    .point-desc {
        display: block;
        margin-top: 3px;
        font-size: 12.5px;
        line-height: 1.5;
        color: var(--zc-foreground-subtle);
        white-space: nowrap;
    }
}

.brand-footer {
    position: relative;
    z-index: 1;
    display: flex;
    flex-wrap: wrap;
    gap: 8px 18px;
    align-items: center;
    font-size: 13px;
    color: var(--zc-foreground-subtlest);

    a {
        color: inherit;
        text-decoration: none;
    }
}

.form-panel {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 72px clamp(20px, 4vw, 48px) 36px;
    background: var(--zc-bg);
}

.login-toolbar {
    position: absolute;
    top: 22px;
    right: 26px;
    z-index: 10;
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    align-items: center;
    gap: 10px;
}

.toolbar-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 34px;
    padding: 0 13px;
    font-size: 13px;
    font-weight: 500;
    color: var(--zc-foreground);
    background: var(--zc-card);
    border: 1px solid var(--zc-border);
    border-radius: 999px;
    cursor: pointer;
    transition:
        border-color 0.16s ease,
        background-color 0.16s ease;

    &:hover {
        border-color: var(--zc-border-hover);
    }
}

.form-wrapper {
    width: 100%;
    max-width: 432px;
}

.form-card {
    padding: clamp(26px, 3.2vw, 36px);
    background: var(--zc-card);
    border: 1px solid var(--zc-border);
    border-radius: 20px;
    box-shadow: 0 16px 36px -12px rgba(0, 0, 0, 0.18);
}

.form-brand {
    display: flex;
    gap: 12px;
    align-items: center;
    padding-bottom: 20px;
    margin-bottom: 22px;
    border-bottom: 1px solid var(--zc-border);

    .form-brand-logo {
        display: flex;
        flex-shrink: 0;
        align-items: center;
        justify-content: center;
        width: 44px;
        height: 44px;
        border-radius: 12px;
        background: var(--zc-tile-bg);
        border: 1px solid var(--zc-tile-border);

        img {
            width: 28px;
            height: 28px;
            object-fit: contain;
        }
    }

    .form-brand-meta {
        display: flex;
        flex-direction: column;
        gap: 2px;
        min-width: 0;
    }

    .form-brand-name {
        font-family: var(--rm-brand-font);
        font-size: 15.5px;
        font-weight: 800;
        color: var(--zc-title);
        letter-spacing: -0.02em;
    }

    .form-brand-sub {
        font-family: var(--rm-brand-font);
        font-size: 10.5px;
        font-weight: 700;
        letter-spacing: 0.12em;
        color: var(--zc-foreground-subtlest);
    }
}

.form-header {
    margin-bottom: 24px;

    h1 {
        margin: 0 0 8px;
        font-size: 24px;
        font-weight: 700;
        line-height: 1.25;
        color: var(--zc-title);
        letter-spacing: -0.02em;
    }

    p {
        margin: 0;
        font-size: 13.5px;
        line-height: 1.55;
        color: var(--zc-foreground-subtle);
    }
}

.form-options {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: 4px 0 20px;
}

.preset-bar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 10px;
    align-items: center;
    padding: 10px 12px;
    margin: 0 0 18px;
    background: var(--zc-input-bg);
    border: 1px solid var(--zc-border);
    border-radius: 12px;
}

.preset-icon {
    flex-shrink: 0;
    color: var(--zc-foreground-subtle);
}

.preset-tip {
    flex: 1;
    min-width: 0;
    font-size: 12.5px;
    line-height: 1.5;
    color: var(--zc-foreground-subtle);
}

.preset-fill {
    flex-shrink: 0;
    height: 28px;
    padding: 0 12px;
    font-size: 12.5px;
    font-weight: 500;
    color: var(--zc-title);
    background: var(--zc-card);
    border: 1px solid var(--zc-border);
    border-radius: 999px;
    cursor: pointer;
    transition:
        border-color 0.16s ease,
        background-color 0.16s ease;

    &:hover {
        border-color: var(--zc-border-hover);
    }
}

.form-footer {
    display: none;
    flex-wrap: wrap;
    justify-content: center;
    gap: 8px 16px;
    margin-top: 24px;
    font-size: 12.5px;
    color: var(--zc-foreground-subtlest);

    a {
        color: inherit;
        text-decoration: none;
    }
}

:deep(.el-form-item) {
    margin-bottom: 24px;
}

:deep(.el-input) {
    --el-input-height: 48px;
    --el-input-text-color: var(--zc-title);
    --el-input-placeholder-color: var(--zc-foreground-subtlest);
    --el-input-border-radius: 12px;
    --el-input-border-color: var(--zc-border);
    --el-input-hover-border-color: var(--zc-border-hover);
    --el-input-focus-border-color: var(--zc-brand);
    --el-input-icon-color: var(--zc-foreground-subtle);

    font-size: 14.5px;
}

:deep(.el-form-item.is-error .el-input__wrapper) {
    box-shadow: 0 0 0 1.5px var(--zc-danger) inset;
}

:deep(.el-form-item__error) {
    padding-top: 5px;
    font-size: 12px;
    line-height: 1.3;
    color: var(--zc-danger);
}

:deep(.el-input__wrapper) {
    height: 48px;
    padding: 0 14px;
    background: var(--zc-input-bg);
    border-radius: 12px;
    box-shadow: 0 0 0 1px var(--zc-border) inset;
    transition:
        box-shadow 0.16s ease,
        background-color 0.16s ease;
}

:deep(.el-input__wrapper:hover) {
    box-shadow: 0 0 0 1px var(--zc-border-hover) inset;
}

:deep(.el-input__wrapper.is-focus),
:deep(.el-input__wrapper:focus-within) {
    box-shadow: var(--zc-ring);
}

:deep(.el-form-item.is-error .el-input__wrapper:focus-within) {
    box-shadow:
        0 0 0 1.5px var(--zc-danger) inset,
        0 0 0 4px var(--zc-danger-soft);
}

:deep(.el-input__inner) {
    height: 100%;
    color: var(--zc-title);

    &::placeholder {
        color: var(--zc-foreground-subtlest);
    }

    &:-webkit-autofill,
    &:-webkit-autofill:hover,
    &:-webkit-autofill:focus {
        -webkit-box-shadow: 0 0 0 1000px var(--zc-input-bg) inset !important;
        -webkit-text-fill-color: var(--zc-title) !important;
        caret-color: var(--zc-title);
        transition: background-color 5000s ease-in-out 0s;
    }
}

:deep(.el-input__prefix) {
    margin-right: 8px;
    color: var(--zc-foreground-subtle);
}

:deep(.el-input__suffix) {
    margin-left: 8px;
    color: var(--zc-foreground-subtle);
}

:deep(.el-checkbox) {
    --el-checkbox-checked-bg-color: var(--zc-primary);
    --el-checkbox-checked-input-border-color: var(--zc-primary);
    --el-checkbox-checked-icon-color: var(--zc-primary-foreground);
    --el-checkbox-checked-text-color: var(--zc-foreground);
    --el-checkbox-text-color: var(--zc-foreground-subtle);
    --el-checkbox-input-border-color-hover: var(--zc-border-hover);
    --el-checkbox-input-border: 1px solid var(--zc-border);

    height: auto;
    font-size: 13.5px;
}

:deep(.el-checkbox__label) {
    font-size: 13.5px;
    color: var(--zc-foreground-subtle);
}

.submit-btn.el-button {
    --el-button-bg-color: var(--zc-primary);
    --el-button-text-color: var(--zc-primary-foreground);
    --el-button-hover-bg-color: var(--zc-primary-hover);
    --el-button-hover-text-color: var(--zc-primary-foreground);
    --el-button-active-bg-color: var(--zc-primary-hover);
    --el-button-active-text-color: var(--zc-primary-foreground);
    --el-button-border-color: transparent;
    --el-button-hover-border-color: transparent;
    --el-button-active-border-color: transparent;

    width: 100%;
    height: 48px;
    margin-top: 2px;
    font-size: 15px;
    font-weight: 600;
    letter-spacing: 0.02em;
    border: none;
    border-radius: 12px;
    transition:
        background-color 0.16s ease,
        box-shadow 0.16s ease,
        transform 0.16s ease;

    &:active {
        transform: translateY(1px);
    }

    &:focus-visible {
        box-shadow: 0 0 0 4px var(--zc-brand-soft);
    }
}

@media (max-width: 1023px) {
    .login-page {
        grid-template-columns: 1fr;
    }

    .brand-panel {
        display: none;
    }

    .form-footer {
        display: flex;
    }
}

@media (max-height: 760px) and (min-width: 1024px) {
    .brand-panel {
        padding-top: 32px;
        padding-bottom: 32px;
    }

    .brand-title {
        margin-top: 18px;
    }

    .brand-points {
        margin-top: 18px;
    }
}

@media (prefers-reduced-motion: reduce) {
    .brand-aurora .aurora,
    .brand-logo-sweep > i,
    .preset-fill,
    :deep(.el-input__wrapper),
    .submit-btn.el-button {
        transition: none;
        animation: none;
    }
}
</style>
