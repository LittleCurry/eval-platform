import { createRouter, createWebHistory } from 'vue-router'
import { routes } from './routes'

const router = createRouter({
    history: createWebHistory(import.meta.env.BASE_URL),
    routes,
})

router.afterEach((to) => {
    document.title = `${String(to.meta.title ?? 'Eval Platform')} · Eval Platform`
})

export default router