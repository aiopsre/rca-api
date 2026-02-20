package main

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

func main1() {
	// 1️⃣ 配置明文密码和用户信息
	userID := "test_operator"
	username := "Test Operator"
	teamID := "namespace:test_team"
	password := "Az123456_" // 修改为你希望的密码

	// 2️⃣ 生成 bcrypt hash
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		panic(fmt.Sprintf("failed to generate bcrypt hash: %v", err))
	}

	passwordHash := string(hash)

	// 3️⃣ 生成 SQL
	sql := fmt.Sprintf(`-- SQL 自动生成 - 最小可登录 operator 用户

INSERT INTO rbac_users (
  user_id,
  username,
  password_hash,
  team_id,
  status
) VALUES (
  '%s',
  '%s',
  '%s',
  '%s',
  'active'
)
ON DUPLICATE KEY UPDATE
  username = VALUES(username),
  password_hash = VALUES(password_hash),
  team_id = VALUES(team_id),
  status = VALUES(status),
  updated_at = CURRENT_TIMESTAMP;

INSERT INTO rbac_user_roles (user_id, role_id)
VALUES ('%s', 'operator')
ON DUPLICATE KEY UPDATE
  updated_at = CURRENT_TIMESTAMP;

-- 最小权限集合示例
INSERT INTO rbac_permissions (
  permission_id,
  name,
  action,
  resource
) VALUES
  ('perm_replay', 'Replay', 'POST', '/v1/sessions/{id}/replay'),
  ('perm_followup', 'Follow-up', 'POST', '/v1/sessions/{id}/followup'),
  ('perm_review', 'Review', 'POST', '/v1/sessions/{id}/review'),
  ('perm_assignment', 'Assignment', 'POST', '/v1/sessions/{id}/assignment'),
  ('perm_sla', 'SLA Escalation', 'POST', '/v1/sessions/{id}/sla')
ON DUPLICATE KEY UPDATE
  name = VALUES(name),
  action = VALUES(action),
  resource = VALUES(resource),
  updated_at = CURRENT_TIMESTAMP;

INSERT INTO rbac_role_permissions (role_id, permission_id)
VALUES
  ('operator', 'perm_replay'),
  ('operator', 'perm_followup'),
  ('operator', 'perm_review'),
  ('operator', 'perm_assignment'),
  ('operator', 'perm_sla')
ON DUPLICATE KEY UPDATE
  updated_at = CURRENT_TIMESTAMP;
`, userID, username, passwordHash, teamID, userID)

	// 4️⃣ 输出 SQL
	fmt.Println(sql)

	// 5️⃣ 提示明文密码用于登录
	fmt.Printf("-- 明文密码: %s\n", password)
}
