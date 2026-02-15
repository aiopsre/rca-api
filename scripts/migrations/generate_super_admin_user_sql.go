package main

import (
	"flag"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

type config struct {
	userID          string
	username        string
	teamID          string
	password        string
	roleID          string
	roleName        string
	resourcePattern string
}

func main() {
	cfg := parseFlags()

	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.password), bcrypt.DefaultCost)
	if err != nil {
		panic(fmt.Sprintf("failed to generate bcrypt hash: %v", err))
	}

	fmt.Print(buildSQL(cfg, string(hash)))
	fmt.Printf("-- login_user_id: %s\n", cfg.userID)
	fmt.Printf("-- login_username: %s\n", cfg.username)
	fmt.Printf("-- login_password: %s\n", cfg.password)
}

func parseFlags() config {
	cfg := config{}

	flag.StringVar(&cfg.userID, "user-id", "test_super_admin", "rbac_users.user_id used during login")
	flag.StringVar(&cfg.username, "username", "Test Super Admin", "rbac_users.username")
	flag.StringVar(&cfg.teamID, "team-id", "namespace:test_team", "rbac_users.team_id; comma-separated teams are allowed")
	flag.StringVar(&cfg.password, "password", "ChangeMe123_", "plaintext password to hash with bcrypt")
	flag.StringVar(&cfg.roleID, "role-id", "super_admin", "rbac_roles.role_id to create/bind")
	flag.StringVar(&cfg.roleName, "role-name", "Super Admin", "rbac_roles.display_name")
	flag.StringVar(&cfg.resourcePattern, "resource-pattern", "/v1/*", "rbac_permissions.resource wildcard for the super admin role")
	flag.Parse()

	cfg.userID = strings.TrimSpace(cfg.userID)
	cfg.username = strings.TrimSpace(cfg.username)
	cfg.teamID = strings.TrimSpace(cfg.teamID)
	cfg.password = strings.TrimSpace(cfg.password)
	cfg.roleID = strings.TrimSpace(cfg.roleID)
	cfg.roleName = strings.TrimSpace(cfg.roleName)
	cfg.resourcePattern = strings.TrimSpace(cfg.resourcePattern)

	if cfg.userID == "" {
		panic("user-id is required")
	}
	if cfg.username == "" {
		cfg.username = cfg.userID
	}
	if cfg.password == "" {
		panic("password is required")
	}
	if cfg.roleID == "" {
		panic("role-id is required")
	}
	if cfg.roleName == "" {
		cfg.roleName = cfg.roleID
	}
	if cfg.resourcePattern == "" {
		panic("resource-pattern is required")
	}

	return cfg
}

func buildSQL(cfg config, passwordHash string) string {
	roleDescription := "non-production super admin for RCA operator bootstrap and test data management"
	permissionID := permissionIDForRole(cfg.roleID)
	permissionDescription := fmt.Sprintf("wildcard access for %s on %s", cfg.roleID, cfg.resourcePattern)

	var out strings.Builder
	out.WriteString("-- SQL auto-generated for one RCA super admin user.\n")
	out.WriteString("-- This role intentionally grants wildcard access and should only be used in non-production environments.\n\n")

	fmt.Fprintf(&out, "INSERT INTO rbac_roles (\n")
	fmt.Fprintf(&out, "  role_id,\n")
	fmt.Fprintf(&out, "  display_name,\n")
	fmt.Fprintf(&out, "  description,\n")
	fmt.Fprintf(&out, "  status\n")
	fmt.Fprintf(&out, ") VALUES (\n")
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(cfg.roleID))
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(cfg.roleName))
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(roleDescription))
	fmt.Fprintf(&out, "  'active'\n")
	fmt.Fprintf(&out, ")\n")
	fmt.Fprintf(&out, "ON DUPLICATE KEY UPDATE\n")
	fmt.Fprintf(&out, "  display_name = VALUES(display_name),\n")
	fmt.Fprintf(&out, "  description = VALUES(description),\n")
	fmt.Fprintf(&out, "  status = VALUES(status),\n")
	fmt.Fprintf(&out, "  updated_at = CURRENT_TIMESTAMP;\n\n")

	fmt.Fprintf(&out, "INSERT INTO rbac_permissions (\n")
	fmt.Fprintf(&out, "  permission_id,\n")
	fmt.Fprintf(&out, "  resource,\n")
	fmt.Fprintf(&out, "  action,\n")
	fmt.Fprintf(&out, "  description,\n")
	fmt.Fprintf(&out, "  status\n")
	fmt.Fprintf(&out, ") VALUES (\n")
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(permissionID))
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(cfg.resourcePattern))
	fmt.Fprintf(&out, "  '*',\n")
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(permissionDescription))
	fmt.Fprintf(&out, "  'active'\n")
	fmt.Fprintf(&out, ")\n")
	fmt.Fprintf(&out, "ON DUPLICATE KEY UPDATE\n")
	fmt.Fprintf(&out, "  resource = VALUES(resource),\n")
	fmt.Fprintf(&out, "  action = VALUES(action),\n")
	fmt.Fprintf(&out, "  description = VALUES(description),\n")
	fmt.Fprintf(&out, "  status = VALUES(status),\n")
	fmt.Fprintf(&out, "  updated_at = CURRENT_TIMESTAMP;\n\n")

	fmt.Fprintf(&out, "INSERT INTO rbac_users (\n")
	fmt.Fprintf(&out, "  user_id,\n")
	fmt.Fprintf(&out, "  username,\n")
	fmt.Fprintf(&out, "  password_hash,\n")
	fmt.Fprintf(&out, "  team_id,\n")
	fmt.Fprintf(&out, "  status\n")
	fmt.Fprintf(&out, ") VALUES (\n")
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(cfg.userID))
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(cfg.username))
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(passwordHash))
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(cfg.teamID))
	fmt.Fprintf(&out, "  'active'\n")
	fmt.Fprintf(&out, ")\n")
	fmt.Fprintf(&out, "ON DUPLICATE KEY UPDATE\n")
	fmt.Fprintf(&out, "  username = VALUES(username),\n")
	fmt.Fprintf(&out, "  password_hash = VALUES(password_hash),\n")
	fmt.Fprintf(&out, "  team_id = VALUES(team_id),\n")
	fmt.Fprintf(&out, "  status = VALUES(status),\n")
	fmt.Fprintf(&out, "  updated_at = CURRENT_TIMESTAMP;\n\n")

	fmt.Fprintf(&out, "INSERT INTO rbac_role_permissions (\n")
	fmt.Fprintf(&out, "  role_id,\n")
	fmt.Fprintf(&out, "  permission_id\n")
	fmt.Fprintf(&out, ") VALUES (\n")
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(cfg.roleID))
	fmt.Fprintf(&out, "  '%s'\n", sqlQuote(permissionID))
	fmt.Fprintf(&out, ")\n")
	fmt.Fprintf(&out, "ON DUPLICATE KEY UPDATE\n")
	fmt.Fprintf(&out, "  updated_at = CURRENT_TIMESTAMP;\n\n")

	fmt.Fprintf(&out, "INSERT INTO rbac_user_roles (\n")
	fmt.Fprintf(&out, "  user_id,\n")
	fmt.Fprintf(&out, "  role_id\n")
	fmt.Fprintf(&out, ") VALUES (\n")
	fmt.Fprintf(&out, "  '%s',\n", sqlQuote(cfg.userID))
	fmt.Fprintf(&out, "  '%s'\n", sqlQuote(cfg.roleID))
	fmt.Fprintf(&out, ")\n")
	fmt.Fprintf(&out, "ON DUPLICATE KEY UPDATE\n")
	fmt.Fprintf(&out, "  updated_at = CURRENT_TIMESTAMP;\n\n")

	return out.String()
}

func permissionIDForRole(roleID string) string {
	replacer := strings.NewReplacer(" ", "_", "-", "_", ":", "_", "/", "_")
	return "perm." + replacer.Replace(roleID) + ".all_v1"
}

func sqlQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
