import { ref } from 'vue'

// M1 阶段无登录体系: 固定使用 id=1 的项目。
// M7 接入登录/RBAC 后, 这里改为从用户上下文读取并支持切换。
export const currentProjectId = ref(1)