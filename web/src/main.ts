import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { cssVars } from './utils/palette'

// 语义色挂在 <html> 上(M7-3 配色统一): 组件 scoped CSS 里就能写 var(--ev-error),
// 不用把色值抄进各个 SFC。只做一次, 组件挂载前设置好, 避免首帧闪一下默认色。
for (const [name, value] of Object.entries(cssVars())) {
    document.documentElement.style.setProperty(name, value)
}

createApp(App).use(router).mount('#app')
