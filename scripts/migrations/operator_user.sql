-- SQL 自动生成 - 最小可登录 operator 用户

INSERT INTO rbac_users (
  user_id,
  username,
  password_hash,
  team_id,
  status
) VALUES (
  'test_operator',
  'Test Operator',
  '$2a$10$EHu6CzmrLctoIbHvFAdJoepmWl4umtyeWyhV4iJDvhCyN8yEa7lcG',
  'namespace:test_team',
  'active'
)
ON DUPLICATE KEY UPDATE
  username = VALUES(username),
  password_hash = VALUES(password_hash),
  team_id = VALUES(team_id),
  status = VALUES(status),
  updated_at = CURRENT_TIMESTAMP;

INSERT INTO rbac_user_roles (user_id, role_id)
VALUES ('test_operator', 'operator')
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

-- 明文密码: Az123456_
