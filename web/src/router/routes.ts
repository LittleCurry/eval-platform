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
                path: 'profiles',
                name: 'profiles',
                component: () => import('../views/PipelineProfilesView.vue'),
                meta: { title: '配置模板' },
            },
            {
                path: 'annotations',
                name: 'annotations',
                component: () => import('../views/AnnotationWorkbenchView.vue'),
                meta: { title: '标注工作台' },
            },
            {
                path: 'gold',
                name: 'gold',
                component: () => import('../views/HumanGoldView.vue'),
                meta: { title: '人工金标打分' },
            },
            {
                path: 'calibration',
                name: 'calibration',
                component: () => import('../views/CalibrationView.vue'),
                meta: { title: 'Judge 校准' },
            },
            {
                path: 'closure',
                name: 'closure',
                component: () => import('../views/ClosureView.vue'),
                meta: { title: '标注闭环' },
            },
            {
                path: 'compare',
                name: 'compare',
                component: () => import('../views/CompareView.vue'),
                meta: { title: 'A/B 对比' },
            },
            {
                path: 'forbidden',
                name: 'forbidden',
                component: () => import('../views/ForbiddenView.vue'),
                meta: { title: '权限不足' },
            },
            {
                path: 'users',
                name: 'users',
                component: () => import('../views/UsersView.vue'),
                // M7-1: 只有管理员能进(服务端也会拦, 这里是为了不让人白点)
                meta: { title: '用户管理', minRole: 'admin' },
            },
        ],
    },
    {
        path: '/login',
        name: 'login',
        component: () => import('../views/LoginView.vue'),
        // 公开页: 未登录也能进(否则登录页自己被守卫拦住就死循环了)
        meta: { title: '登录', public: true },
    },
]