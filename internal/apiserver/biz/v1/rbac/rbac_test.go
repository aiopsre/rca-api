package rbac

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

func TestRBACBiz_EnforceByRolePermission(t *testing.T) {
	bizObj, cleanup := newRBACBizTest(t)
	defer cleanup()

	ctx := context.Background()

	_, err := bizObj.UpsertUser(ctx, &UpsertUserRequest{UserID: "alice", Username: "Alice"})
	require.NoError(t, err)
	_, err = bizObj.UpsertRole(ctx, &UpsertRoleRequest{RoleID: "operator", DisplayName: "Operator"})
	require.NoError(t, err)
	_, err = bizObj.UpsertPermission(ctx, &UpsertPermissionRequest{
		PermissionID: "perm.operator.inbox",
		Resource:     "/v1/operator/*",
		Action:       "ai.read",
	})
	require.NoError(t, err)

	_, err = bizObj.AssignRolePermissions(ctx, &AssignRolePermissionsRequest{
		RoleID:        "operator",
		PermissionIDs: []string{"perm.operator.inbox"},
	})
	require.NoError(t, err)
	_, err = bizObj.AssignUserRoles(ctx, &AssignUserRolesRequest{UserID: "alice", RoleIDs: []string{"operator"}})
	require.NoError(t, err)

	allowed, err := bizObj.Enforce(ctx, "alice", "/v1/operator/inbox", "ai.read")
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = bizObj.Enforce(ctx, "alice", "/v1/operator/inbox", "ai.run")
	require.NoError(t, err)
	require.False(t, allowed)

	allowed, err = bizObj.Enforce(ctx, "bob", "/v1/operator/inbox", "ai.read")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestRBACBiz_ResolveLoginProfile(t *testing.T) {
	bizObj, cleanup := newRBACBizTest(t)
	defer cleanup()

	ctx := context.Background()
	password := "pass-123"
	_, err := bizObj.UpsertUser(ctx, &UpsertUserRequest{UserID: "reviewer-a", Username: "Reviewer A", Password: &password, TeamID: "payments"})
	require.NoError(t, err)
	_, err = bizObj.UpsertRole(ctx, &UpsertRoleRequest{RoleID: "reviewer"})
	require.NoError(t, err)
	_, err = bizObj.UpsertPermission(ctx, &UpsertPermissionRequest{
		PermissionID: "perm.review.start",
		Resource:     "/v1/sessions/:sessionID/actions/review-start",
		Action:       "session.review",
	})
	require.NoError(t, err)
	_, err = bizObj.AssignRolePermissions(ctx, &AssignRolePermissionsRequest{RoleID: "reviewer", PermissionIDs: []string{"perm.review.start"}})
	require.NoError(t, err)
	_, err = bizObj.AssignUserRoles(ctx, &AssignUserRolesRequest{UserID: "reviewer-a", RoleIDs: []string{"reviewer"}})
	require.NoError(t, err)

	profile, err := bizObj.ResolveLoginProfile(ctx, &ResolveLoginProfileRequest{UserID: "reviewer-a", Password: password})
	require.NoError(t, err)
	require.NotNil(t, profile)
	require.Equal(t, "reviewer-a", profile.User.UserID)
	require.Equal(t, []string{"reviewer"}, profile.RoleIDs)
	require.Contains(t, profile.EffectiveActions, "session.review")
	require.True(t, profile.PasswordValidated)
	require.Equal(t, []string{"payments"}, profile.EffectiveTeamIDs)

	_, err = bizObj.ResolveLoginProfile(ctx, &ResolveLoginProfileRequest{UserID: "reviewer-a", Password: "wrong"})
	require.ErrorIs(t, err, errno.ErrUnauthenticated)
}

func newRBACBizTest(t *testing.T) (*rbacBiz, func()) {
	t.Helper()
	store.ResetForTest()

	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UTC().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.RBACUserM{},
		&model.RBACRoleM{},
		&model.RBACPermissionM{},
		&model.RBACUserRoleM{},
		&model.RBACRolePermissionM{},
	))

	s := store.NewStore(db)
	bizObj := New(s)
	cleanup := func() {
		store.ResetForTest()
	}
	return bizObj, cleanup
}
