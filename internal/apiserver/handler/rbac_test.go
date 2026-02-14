package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

func TestRBACMiddleware_OperatorInbox(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(
		&model.SessionContextM{},
		&model.SessionHistoryEventM{},
		&model.RBACUserM{},
		&model.RBACRoleM{},
		&model.RBACPermissionM{},
		&model.RBACUserRoleM{},
		&model.RBACRolePermissionM{},
	))
	bootstrapAdminRBAC(t, s)

	adminToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "admin01",
		"scopes":      []string{"ai.read", "ai.run", "config.admin", "rbac.admin", "session.review", "session.assignment"},
	})

	upsertPermissionViaAPI(t, client, baseURL, adminToken, map[string]any{
		"permission_id": "perm.operator.inbox",
		"resource":      "/v1/operator/*",
		"action":        "ai.read",
	})
	upsertRoleViaAPI(t, client, baseURL, adminToken, map[string]any{"role_id": "operator"})
	upsertUserViaAPI(t, client, baseURL, adminToken, map[string]any{"user_id": "alice", "username": "alice"})
	upsertUserViaAPI(t, client, baseURL, adminToken, map[string]any{"user_id": "bob", "username": "bob"})
	assignRolePermissionsViaAPI(t, client, baseURL, adminToken, "operator", []string{"perm.operator.inbox"})
	assignUserRolesViaAPI(t, client, baseURL, adminToken, "alice", []string{"operator"})

	aliceToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{"operator_id": "alice"})
	status, body, err := doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/inbox", baseURL), nil, map[string]string{
		"Authorization": "Bearer " + aliceToken,
	})
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, status, "body=%s", string(body))

	bobToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{"operator_id": "bob", "scopes": []string{"ai.read"}})
	status, body, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/inbox", baseURL), nil, map[string]string{
		"Authorization": "Bearer " + bobToken,
	})
	require.NoError(t, err)
	require.Equalf(t, http.StatusForbidden, status, "body=%s", string(body))
}

func TestRBACMiddleware_ReviewerCanReviewButCannotReplay(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(
		&model.SessionContextM{},
		&model.SessionHistoryEventM{},
		&model.RBACUserM{},
		&model.RBACRoleM{},
		&model.RBACPermissionM{},
		&model.RBACUserRoleM{},
		&model.RBACRolePermissionM{},
	))
	bootstrapAdminRBAC(t, s)

	adminToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "admin01",
		"scopes":      []string{"ai.read", "ai.run", "config.admin", "rbac.admin", "session.review", "session.assignment"},
	})

	upsertPermissionViaAPI(t, client, baseURL, adminToken, map[string]any{
		"permission_id": "perm.review.start",
		"resource":      "/v1/sessions/:sessionID/actions/review-start",
		"action":        "session.review",
	})
	upsertRoleViaAPI(t, client, baseURL, adminToken, map[string]any{"role_id": "reviewer"})
	upsertUserViaAPI(t, client, baseURL, adminToken, map[string]any{"user_id": "reviewer-a", "username": "reviewer-a"})
	assignRolePermissionsViaAPI(t, client, baseURL, adminToken, "reviewer", []string{"perm.review.start"})
	assignUserRolesViaAPI(t, client, baseURL, adminToken, "reviewer-a", []string{"reviewer"})

	reviewerToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "reviewer-a",
		"scopes":      []string{"ai.run", "session.review"},
	})

	status, body, err := doJSONRequestWithHeaders(client, http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/sess-rbac/actions/replay", baseURL), []byte(`{}`), map[string]string{
			"Authorization": "Bearer " + reviewerToken,
		})
	require.NoError(t, err)
	require.Equalf(t, http.StatusForbidden, status, "body=%s", string(body))

	status, body, err = doJSONRequestWithHeaders(client, http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/sess-rbac/actions/review-start", baseURL), []byte(`{}`), map[string]string{
			"Authorization": "Bearer " + reviewerToken,
		})
	require.NoError(t, err)
	require.NotEqualf(t, http.StatusForbidden, status, "body=%s", string(body))
}

func upsertPermissionViaAPI(t *testing.T, client *http.Client, baseURL string, token string, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	status, body, err := doJSONRequestWithHeaders(client, http.MethodPost, fmt.Sprintf("%s/v1/permissions", baseURL), raw, map[string]string{
		"Authorization": "Bearer " + token,
	})
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, status, "body=%s", string(body))
}

func upsertRoleViaAPI(t *testing.T, client *http.Client, baseURL string, token string, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	status, body, err := doJSONRequestWithHeaders(client, http.MethodPost, fmt.Sprintf("%s/v1/roles", baseURL), raw, map[string]string{
		"Authorization": "Bearer " + token,
	})
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, status, "body=%s", string(body))
}

func upsertUserViaAPI(t *testing.T, client *http.Client, baseURL string, token string, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	status, body, err := doJSONRequestWithHeaders(client, http.MethodPost, fmt.Sprintf("%s/v1/users", baseURL), raw, map[string]string{
		"Authorization": "Bearer " + token,
	})
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, status, "body=%s", string(body))
}

func assignRolePermissionsViaAPI(t *testing.T, client *http.Client, baseURL string, token string, roleID string, permissionIDs []string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"permission_ids": permissionIDs})
	require.NoError(t, err)
	status, body, err := doJSONRequestWithHeaders(client, http.MethodPost,
		fmt.Sprintf("%s/v1/roles/%s/permissions", baseURL, roleID), raw, map[string]string{
			"Authorization": "Bearer " + token,
		})
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, status, "body=%s", string(body))
}

func assignUserRolesViaAPI(t *testing.T, client *http.Client, baseURL string, token string, userID string, roleIDs []string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"role_ids": roleIDs})
	require.NoError(t, err)
	status, body, err := doJSONRequestWithHeaders(client, http.MethodPost,
		fmt.Sprintf("%s/v1/users/%s/roles", baseURL, userID), raw, map[string]string{
			"Authorization": "Bearer " + token,
		})
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, status, "body=%s", string(body))
}

func bootstrapAdminRBAC(t *testing.T, s store.IStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, s.RBAC().UpsertUser(ctx, &model.RBACUserM{
		UserID:    "admin01",
		Username:  "admin01",
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}))
	require.NoError(t, s.RBAC().UpsertRole(ctx, &model.RBACRoleM{
		RoleID:      "admin",
		DisplayName: "Admin",
		Status:      "active",
		CreatedAt:   now,
		UpdatedAt:   now,
	}))
	require.NoError(t, s.RBAC().UpsertPermission(ctx, &model.RBACPermissionM{
		PermissionID: "perm.admin.rbac",
		Resource:     "/v1/*",
		Action:       "rbac.admin",
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
	}))
	require.NoError(t, s.RBAC().UpsertPermission(ctx, &model.RBACPermissionM{
		PermissionID: "perm.admin.config",
		Resource:     "/v1/config/*",
		Action:       "config.admin",
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
	}))
	require.NoError(t, s.RBAC().ReplaceRolePermissions(ctx, "admin", []string{"perm.admin.rbac", "perm.admin.config"}))
	require.NoError(t, s.RBAC().ReplaceUserRoles(ctx, "admin01", []string{"admin"}))
}
