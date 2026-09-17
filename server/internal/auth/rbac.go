package auth

// RBAC 权限矩阵(M7-1)。
//
// 为什么是一张"动作 × 角色"的表, 而不是在路由上散装写 `if role != "admin"`:
// 权限的难点从来不是"拦不拦得住", 而是**一致**: 新增一个端点时, 散装判断必然漏。
// 这里把动作收敛成 5 个, 每个端点声明自己要哪个动作, 漏了就有一个测试会发现
// (TestEveryProtectedRouteDeclaresAction 走一遍路由表)。
//
// 角色定位:
//   - viewer: 只读。看报告、看对比、看标注结果 —— 同事要数据时给这个;
//   - editor: 能跑实验、能标注、能改金标。日常干活的人;
//   - admin:  还能删数据与管人。删数据是不可逆动作, 单独一档。

// 动作(端点需要的能力)。
const (
	// ActionRead 看数据(GET)。
	ActionRead = "read"
	// ActionWrite 建/改数据: 导入语料、建数据集、打标、打分、存配置模板。
	ActionWrite = "write"
	// ActionSubmit 提交评测任务(会花钱调 LLM, 所以单独一档: 可以给"能看能改"的人,
	// 但不必给"只想试试"的人)。
	ActionSubmit = "submit"
	// ActionDelete 删数据(不可逆)。只给 admin。
	ActionDelete = "delete"
	// ActionAdmin 管项目与账号(建项目、加成员、改角色、停用账号)。
	ActionAdmin = "admin"
)

// RoleRank 角色的能力档位: 数字越大能力越强。仅用于"至少需要某档"的判断。
var RoleRank = map[string]int{
	"viewer": 1,
	"editor": 2,
	"admin":  3,
}

// roleActions 每个角色允许的动作集合。
//
// viewer 的 write 是 false —— 注意"标注"也算 write: 标注是产生人工结论的动作,
// 不该让只读账号写。admin 用 RoleRank 兜底, 这里也显式列全, 便于一眼看完整张表。
var roleActions = map[string]map[string]bool{
	"viewer": {
		ActionRead: true,
	},
	"editor": {
		ActionRead:   true,
		ActionWrite:  true,
		ActionSubmit: true,
	},
	"admin": {
		ActionRead:   true,
		ActionWrite:  true,
		ActionSubmit: true,
		ActionDelete: true,
		ActionAdmin:  true,
	},
}

// ValidRole 角色是否是合法枚举(与 DB 的 CHECK 一致)。
func ValidRole(role string) bool {
	_, ok := RoleRank[role]
	return ok
}

// Can 判断角色能否执行某动作。未知角色一律 false —— 默认拒绝, 不是默认放行。
func Can(role, action string) bool {
	actions, ok := roleActions[role]
	if !ok {
		return false
	}
	return actions[action]
}

// AtLeast 判断 role 是否达到 min 的能力档(用于"至少 editor"这类路由约束)。
func AtLeast(role, min string) bool {
	roleRank, ok := RoleRank[role]
	if !ok {
		return false
	}
	minRank, ok := RoleRank[min]
	if !ok {
		return false
	}
	return roleRank >= minRank
}

// ActionsOf 列出某角色允许的动作(前端用来决定按钮显不显示)。
func ActionsOf(role string) []string {
	out := make([]string, 0, len(roleActions[role]))
	for _, action := range []string{ActionRead, ActionWrite, ActionSubmit, ActionDelete, ActionAdmin} {
		if Can(role, action) {
			out = append(out, action)
		}
	}
	return out
}
