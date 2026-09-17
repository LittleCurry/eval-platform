<script setup lang="ts">
// 统一的"加载 / 出错 / 空"三态(M7-3)。
//
// 为什么要抽出来: 之前每个列表页各写一份 `errorText` alert + `loading` 透传给表格,
// 结果同样是"拉不到数据", 有的页面给红条、有的只留一张空表 —— 用户分不清
// "没有数据"和"请求失败了"。这里把三种状态收敛成一处, 并在出错时**给一个重试按钮**
// (没有重试就只能刷整页, 体验差且看不出是不是偶发)。

import { NAlert, NButton, NEmpty, NSkeleton } from 'naive-ui'

const props = withDefaults(defineProps<{
    loading?: boolean
    error?: string
    /** 已加载但结果为空(注意: 加载中不算空, 否则会先闪一下"暂无数据")。 */
    empty?: boolean
    emptyText?: string
    /** 骨架屏行数(首次加载时用; 已有数据时刷新不再闪骨架)。 */
    skeletonRows?: number
    retryText?: string
}>(), {
    loading: false,
    error: '',
    empty: false,
    emptyText: '暂无数据',
    skeletonRows: 4,
    retryText: '重试',
})

const emit = defineEmits<{ retry: [] }>()
</script>

<template>
    <div>
        <NAlert v-if="props.error" type="error" :show-icon="false" style="margin-bottom: 12px">
            <div style="display: flex; align-items: center; justify-content: space-between; gap: 12px">
                <span>{{ props.error }}</span>
                <NButton size="tiny" quaternary @click="emit('retry')">{{ props.retryText }}</NButton>
            </div>
        </NAlert>

        <!-- 首次加载: 骨架屏比"空白 + 转圈"更能说明"这里将会出现一张表" -->
        <NSkeleton v-if="props.loading" text :repeat="props.skeletonRows" />

        <!-- 加载完成且确实为空: 说清是空而不是失败 -->
        <NEmpty
            v-else-if="props.empty && !props.error"
            :description="props.emptyText"
            style="margin: 24px 0"
        />

        <slot v-else />
    </div>
</template>
