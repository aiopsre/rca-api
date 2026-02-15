-- SQL auto-generated for one RCA super admin user.
-- This role intentionally grants wildcard access and should only be used in non-production environments.

INSERT INTO rbac_roles (
  role_id,
  display_name,
  description,
  status
) VALUES (
  'super_admin',
  'Super Admin',
  'non-production super admin for RCA operator bootstrap and test data management',
  'active'
)
ON DUPLICATE KEY UPDATE
  display_name = VALUES(display_name),
  description = VALUES(description),
  status = VALUES(status),
  updated_at = CURRENT_TIMESTAMP;

INSERT INTO rbac_permissions (
  permission_id,
  resource,
  action,
  description,
  status
) VALUES (
  'perm.super_admin.all_v1',
  '/v1/*',
  '*',
  'wildcard access for super_admin on /v1/*',
  'active'
)
ON DUPLICATE KEY UPDATE
  resource = VALUES(resource),
  action = VALUES(action),
  description = VALUES(description),
  status = VALUES(status),
  updated_at = CURRENT_TIMESTAMP;

INSERT INTO rbac_users (
  user_id,
  username,
  password_hash,
  team_id,
  status
) VALUES (
  'bootstrap_super_admin',
  'Bootstrap Super Admin',
  '$2a$10$kAt.oReq9rc/Ugtb5fq8suxKtxVTQlorrzLMmwxbY0Z5dVieofPGC',
  'namespace:test_team',
  'active'
)
ON DUPLICATE KEY UPDATE
  username = VALUES(username),
  password_hash = VALUES(password_hash),
  team_id = VALUES(team_id),
  status = VALUES(status),
  updated_at = CURRENT_TIMESTAMP;

INSERT INTO rbac_role_permissions (
  role_id,
  permission_id
) VALUES (
  'super_admin',
  'perm.super_admin.all_v1'
)
ON DUPLICATE KEY UPDATE
  updated_at = CURRENT_TIMESTAMP;

INSERT INTO rbac_user_roles (
  user_id,
  role_id
) VALUES (
  'bootstrap_super_admin',
  'super_admin'
)
ON DUPLICATE KEY UPDATE
  updated_at = CURRENT_TIMESTAMP;

-- login_user_id: bootstrap_super_admin
-- login_username: Bootstrap Super Admin
-- login_password: Admin123_
