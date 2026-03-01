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
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

func TestRBACBiz_EnforceByRolePermission(t *testing.T) {
	bizObj, cleanup := newRBACBizTest(t)
	defer cleanup()

	ctx := context.Background()

	_, err := bizObj.UpsertUser(ctx, &v1.UpsertUserRequest{UserId: "alice", Username: "Alice"})
	require.NoError(t, err)
	_, err = bizObj.UpsertRole(ctx, &v1.UpsertRoleRequest{RoleId: "operator", DisplayName: "Operator"})
	require.NoError(t, err)
	_, err = bizObj.UpsertPermission(ctx, &v1.UpsertPermissionRequest{
		PermissionId: "perm.operator.inbox",
		Resource:     "/v1/operator/*",
		Action:       "ai.read",
	})
	require.NoError(t, err)

	_, err = bizObj.AssignRolePermissions(ctx, &v1.AssignRolePermissionsRequest{
		RoleId:        "operator",
		PermissionIds: []string{"perm.operator.inbox"},
	})
	require.NoError(t, err)
	_, err = bizObj.AssignUserRoles(ctx, &v1.AssignUserRolesRequest{UserId: "alice", RoleIds: []string{"operator"}})
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
	_, err := bizObj.UpsertUser(ctx, &v1.UpsertUserRequest{UserId: "reviewer-a", Username: "Reviewer A", Password: &password, TeamId: "payments"})
	require.NoError(t, err)
	_, err = bizObj.UpsertRole(ctx, &v1.UpsertRoleRequest{RoleId: "reviewer"})
	require.NoError(t, err)
	_, err = bizObj.UpsertPermission(ctx, &v1.UpsertPermissionRequest{
		PermissionId: "perm.review.start",
		Resource:     "/v1/sessions/:sessionID/actions/review-start",
		Action:       "session.review",
	})
	require.NoError(t, err)
	_, err = bizObj.AssignRolePermissions(ctx, &v1.AssignRolePermissionsRequest{RoleId: "reviewer", PermissionIds: []string{"perm.review.start"}})
	require.NoError(t, err)
	_, err = bizObj.AssignUserRoles(ctx, &v1.AssignUserRolesRequest{UserId: "reviewer-a", RoleIds: []string{"reviewer"}})
	require.NoError(t, err)

	resp, err := bizObj.ResolveLoginProfile(ctx, &v1.ResolveLoginProfileRequest{UserId: "reviewer-a", Password: password})
	require.NoError(t, err)
	require.NotNil(t, resp)
	profile := resp.GetLoginProfile()
	require.NotNil(t, profile)
	require.Equal(t, "reviewer-a", profile.User.GetUserId())
	require.Equal(t, []string{"reviewer"}, profile.GetRoleIds())
	require.Contains(t, profile.GetEffectiveActions(), "session.review")
	require.True(t, profile.GetPasswordValidated())
	require.Equal(t, []string{"payments"}, profile.GetEffectiveTeamIds())

	_, err = bizObj.ResolveLoginProfile(ctx, &v1.ResolveLoginProfileRequest{UserId: "reviewer-a", Password: "wrong"})
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