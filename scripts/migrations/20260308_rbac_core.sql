-- RBAC core schema for RCA apiserver (MySQL/MariaDB)

CREATE TABLE IF NOT EXISTS rbac_users (
  id BIGINT NOT NULL AUTO_INCREMENT,
  user_id VARCHAR(64) NOT NULL,
  username VARCHAR(128) NOT NULL DEFAULT '',
  password_hash VARCHAR(255) NULL,
  team_id VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_rbac_users_user_id (user_id),
  KEY idx_rbac_users_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS rbac_roles (
  id BIGINT NOT NULL AUTO_INCREMENT,
  role_id VARCHAR(64) NOT NULL,
  display_name VARCHAR(128) NOT NULL DEFAULT '',
  description TEXT NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_rbac_roles_role_id (role_id),
  KEY idx_rbac_roles_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS rbac_permissions (
  id BIGINT NOT NULL AUTO_INCREMENT,
  permission_id VARCHAR(96) NOT NULL,
  resource VARCHAR(255) NOT NULL,
  action VARCHAR(64) NOT NULL,
  description TEXT NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_rbac_permissions_permission_id (permission_id),
  KEY idx_rbac_permissions_resource_action (resource, action),
  KEY idx_rbac_permissions_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS rbac_user_roles (
  id BIGINT NOT NULL AUTO_INCREMENT,
  user_id VARCHAR(64) NOT NULL,
  role_id VARCHAR(64) NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_rbac_user_role (user_id, role_id),
  KEY idx_rbac_user_roles_user_id (user_id),
  KEY idx_rbac_user_roles_role_id (role_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS rbac_role_permissions (
  id BIGINT NOT NULL AUTO_INCREMENT,
  role_id VARCHAR(64) NOT NULL,
  permission_id VARCHAR(96) NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_rbac_role_permission (role_id, permission_id),
  KEY idx_rbac_role_permissions_role_id (role_id),
  KEY idx_rbac_role_permissions_permission_id (permission_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Minimal seed example
INSERT INTO rbac_roles (role_id, display_name, description, status)
VALUES ('operator', 'Operator', 'can read inbox/workbench and run replay/follow-up', 'active')
ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), description = VALUES(description), status = VALUES(status);

INSERT INTO rbac_roles (role_id, display_name, description, status)
VALUES ('reviewer', 'Reviewer', 'can perform review actions', 'active')
ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), description = VALUES(description), status = VALUES(status);

INSERT INTO rbac_roles (role_id, display_name, description, status)
VALUES ('admin', 'Admin', 'can manage config and rbac policy', 'active')
ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), description = VALUES(description), status = VALUES(status);
