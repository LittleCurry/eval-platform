package auth

import "testing"

// TestPermissionMatrix 权限矩阵逐格断言。
//
// 这张表是"谁能干什么"的唯一真相源: 矩阵写错一个格子, 就是一个越权入口。
func TestPermissionMatrix(t *testing.T) {
	cases := []struct {
		role    string
		action  string
		allowed bool
	}{
		// viewer: 只读
		{"viewer", ActionRead, true},
		{"viewer", ActionWrite, false},
		{"viewer", ActionSubmit, false},
		{"viewer", ActionDelete, false},
		{"viewer", ActionAdmin, false},

		// editor: 能干活(改数据/标注/跑实验), 不能删数据也不管人
		{"editor", ActionRead, true},
		{"editor", ActionWrite, true},
		{"editor", ActionSubmit, true},
		{"editor", ActionDelete, false},
		{"editor", ActionAdmin, false},

		// admin: 全部
		{"admin", ActionRead, true},
		{"admin", ActionWrite, true},
		{"admin", ActionSubmit, true},
		{"admin", ActionDelete, true},
		{"admin", ActionAdmin, true},

		// 未知/空角色: 默认拒绝(不是默认放行)
		{"", ActionRead, false},
		{"superuser", ActionRead, false},
		{"ADMIN", ActionAdmin, false}, // 大小写敏感: 别让 "Admin" 悄悄变成管理员
	}
	for _, item := range cases {
		if got := Can(item.role, item.action); got != item.allowed {
			t.Fatalf("Can(%q, %q) = %v, want %v", item.role, item.action, got, item.allowed)
		}
	}
}

func TestAtLeastAndRoleHelpers(t *testing.T) {
	if !AtLeast("admin", "editor") || !AtLeast("editor", "editor") {
		t.Fatal("admin/editor 都应达到 editor 档")
	}
	if AtLeast("viewer", "editor") {
		t.Fatal("viewer 不该达到 editor 档")
	}
	if AtLeast("nobody", "viewer") || AtLeast("viewer", "nobody") {
		t.Fatal("未知角色/未知档位都必须返回 false")
	}
	if !ValidRole("viewer") || !ValidRole("editor") || !ValidRole("admin") {
		t.Fatal("三个角色都应合法")
	}
	if ValidRole("root") {
		t.Fatal("root 不是合法角色")
	}
}

func TestActionsOf(t *testing.T) {
	viewer := ActionsOf("viewer")
	if len(viewer) != 1 || viewer[0] != ActionRead {
		t.Fatalf("viewer 只应有 read: %v", viewer)
	}
	if len(ActionsOf("editor")) != 3 {
		t.Fatalf("editor 应有 3 个动作: %v", ActionsOf("editor"))
	}
	if len(ActionsOf("admin")) != 5 {
		t.Fatalf("admin 应有全部 5 个动作: %v", ActionsOf("admin"))
	}
	if len(ActionsOf("ghost")) != 0 {
		t.Fatalf("未知角色没有任何动作: %v", ActionsOf("ghost"))
	}
}
