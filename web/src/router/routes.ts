import type { RouteRecordRaw } from 'vue-router'

// 组件用懒加载: 首屏不下载, 构建时自动分包
export const routes: RouteRecordRaw[] = [
    {
        path: '/',
        component: () => import('../layouts/DefaultLayout.vue'),
        children: [
            {
                path: '',
                name: 'home',
                component: () => import('../views/HomeView.vue'),
                meta: { title: '概览' },
            },
            {
                path: 'corpora',
                name: 'corpora',
                component: () => import('../views/CorpusListView.vue'),
                meta: { title: '语料库' },
            },
            {
                path: 'datasets',
                name: 'datasets',
                component: () => import('../views/DatasetListView.vue'),
                meta: { title: '数据集' },
            },
            {
                path: 'datasets/:id',
                name: 'dataset-detail',
                component: () => import('../views/DatasetDetailView.vue'),
                meta: { title: '数据集详情' },
            },
            {
                path: 'runs',
                name: 'runs',
                component: () => import('../views/RunsView.vue'),
                meta: { title: '运行报告' },
            },
            {
                path: 'runs/:id',
                name: 'run-detail',
                component: () => import('../views/RunReportView.vue'),
                meta: { title: '运行详情' },
            },
            {
                path: 'compare',
                name: 'compare',
                component: () => import('../views/CompareView.vue'),
                meta: { title: 'A/B 对比' },
            },
        ],
    },
    {
        path: '/login',
        name: 'login',
        component: () => import('../views/LoginView.vue'),
        meta: { title: '登录' },
    },
]