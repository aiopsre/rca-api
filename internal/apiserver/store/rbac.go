package store

import (
	"context"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

const (
	rbacStatusActive     = "active"
	rbacDefaultListLimit = 50
	rbacMaxListLimit     = 200
)

type RBACPolicyRow struct {
	RoleID   string
	Resource string
	Action   string
}

type RBACGroupingRow struct {
	UserID string
	RoleID string
}

type RBACStore interface {
	GetUser(ctx context.Context, userID string) (*model.RBACUserM, error)
	ListUsers(ctx context.Context, offset int, limit int) (int64, []*model.RBACUserM, error)
	UpsertUser(ctx context.Context, obj *model.RBACUserM) error
	DeleteUser(ctx context.Context, userID string) error

	GetRole(ctx context.Context, roleID string) (*model.RBACRoleM, error)
	ListRoles(ctx context.Context, offset int, limit int) (int64, []*model.RBACRoleM, error)
	UpsertRole(ctx context.Context, obj *model.RBACRoleM) error
	DeleteRole(ctx context.Context, roleID string) error

	GetPermission(ctx context.Context, permissionID string) (*model.RBACPermissionM, error)
	ListPermissions(ctx context.Context, offset int, limit int) (int64, []*model.RBACPermissionM, error)
	UpsertPermission(ctx context.Context, obj *model.RBACPermissionM) error
	DeletePermission(ctx context.Context, permissionID string) error

	ReplaceUserRoles(ctx context.Context, userID string, roleIDs []string) error
	ListRoleIDsByUser(ctx context.Context, userID string) ([]string, error)

	ReplaceRolePermissions(ctx context.Context, roleID string, permissionIDs []string) error
	ListPermissionsByRole(ctx context.Context, roleID string) ([]*model.RBACPermissionM, error)

	ListPolicyRows(ctx context.Context) ([]*RBACPolicyRow, error)
	ListGroupingRows(ctx context.Context) ([]*RBACGroupingRow, error)
}

type rbacStore struct {
	s *store
}

func newRBACStore(s *store) *rbacStore {
	return &rbacStore{s: s}
}

func (rs *rbacStore) GetUser(ctx context.Context, userID string) (*model.RBACUserM, error) {
	out := &model.RBACUserM{}
	err := rs.s.DB(ctx).Model(&model.RBACUserM{}).
		Where("user_id = ?", strings.TrimSpace(userID)).
		First(out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (rs *rbacStore) ListUsers(ctx context.Context, offset int, limit int) (int64, []*model.RBACUserM, error) {
	query := rs.s.DB(ctx).Model(&model.RBACUserM{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, nil, err
	}
	out := make([]*model.RBACUserM, 0)
	err := query.Order("id ASC").Offset(normalizeOffset(offset)).Limit(normalizeLimit(limit)).Find(&out).Error
	if err != nil {
		return 0, nil, err
	}
	return total, out, nil
}

func (rs *rbacStore) UpsertUser(ctx context.Context, obj *model.RBACUserM) error {
	if obj == nil {
		return nil
	}
	return rs.s.DB(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"username",
				"password_hash",
				"team_id",
				"status",
				"updated_at",
			}),
		}).
		Create(obj).Error
}

func (rs *rbacStore) DeleteUser(ctx context.Context, userID string) error {
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" {
		return nil
	}
	return rs.s.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", trimmed).Delete(&model.RBACUserRoleM{}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", trimmed).Delete(&model.RBACUserM{}).Error
	})
}

func (rs *rbacStore) GetRole(ctx context.Context, roleID string) (*model.RBACRoleM, error) {
	out := &model.RBACRoleM{}
	err := rs.s.DB(ctx).Model(&model.RBACRoleM{}).
		Where("role_id = ?", strings.TrimSpace(roleID)).
		First(out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (rs *rbacStore) ListRoles(ctx context.Context, offset int, limit int) (int64, []*model.RBACRoleM, error) {
	query := rs.s.DB(ctx).Model(&model.RBACRoleM{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, nil, err
	}
	out := make([]*model.RBACRoleM, 0)
	err := query.Order("id ASC").Offset(normalizeOffset(offset)).Limit(normalizeLimit(limit)).Find(&out).Error
	if err != nil {
		return 0, nil, err
	}
	return total, out, nil
}

func (rs *rbacStore) UpsertRole(ctx context.Context, obj *model.RBACRoleM) error {
	if obj == nil {
		return nil
	}
	return rs.s.DB(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "role_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"display_name",
				"description",
				"status",
				"updated_at",
			}),
		}).
		Create(obj).Error
}

func (rs *rbacStore) DeleteRole(ctx context.Context, roleID string) error {
	trimmed := strings.TrimSpace(roleID)
	if trimmed == "" {
		return nil
	}
	return rs.s.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", trimmed).Delete(&model.RBACUserRoleM{}).Error; err != nil {
			return err
		}
		if err := tx.Where("role_id = ?", trimmed).Delete(&model.RBACRolePermissionM{}).Error; err != nil {
			return err
		}
		return tx.Where("role_id = ?", trimmed).Delete(&model.RBACRoleM{}).Error
	})
}

func (rs *rbacStore) GetPermission(ctx context.Context, permissionID string) (*model.RBACPermissionM, error) {
	out := &model.RBACPermissionM{}
	err := rs.s.DB(ctx).Model(&model.RBACPermissionM{}).
		Where("permission_id = ?", strings.TrimSpace(permissionID)).
		First(out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (rs *rbacStore) ListPermissions(ctx context.Context, offset int, limit int) (int64, []*model.RBACPermissionM, error) {
	query := rs.s.DB(ctx).Model(&model.RBACPermissionM{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, nil, err
	}
	out := make([]*model.RBACPermissionM, 0)
	err := query.Order("id ASC").Offset(normalizeOffset(offset)).Limit(normalizeLimit(limit)).Find(&out).Error
	if err != nil {
		return 0, nil, err
	}
	return total, out, nil
}

func (rs *rbacStore) UpsertPermission(ctx context.Context, obj *model.RBACPermissionM) error {
	if obj == nil {
		return nil
	}
	return rs.s.DB(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "permission_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"resource",
				"action",
				"description",
				"status",
				"updated_at",
			}),
		}).
		Create(obj).Error
}

func (rs *rbacStore) DeletePermission(ctx context.Context, permissionID string) error {
	trimmed := strings.TrimSpace(permissionID)
	if trimmed == "" {
		return nil
	}
	return rs.s.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("permission_id = ?", trimmed).Delete(&model.RBACRolePermissionM{}).Error; err != nil {
			return err
		}
		return tx.Where("permission_id = ?", trimmed).Delete(&model.RBACPermissionM{}).Error
	})
}

func (rs *rbacStore) ReplaceUserRoles(ctx context.Context, userID string, roleIDs []string) error {
	trimmedUserID := strings.TrimSpace(userID)
	if trimmedUserID == "" {
		return nil
	}
	cleanRoles := uniqueNonEmpty(roleIDs)
	return rs.s.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", trimmedUserID).Delete(&model.RBACUserRoleM{}).Error; err != nil {
			return err
		}
		if len(cleanRoles) == 0 {
			return nil
		}
		batch := make([]*model.RBACUserRoleM, 0, len(cleanRoles))
		for _, roleID := range cleanRoles {
			batch = append(batch, &model.RBACUserRoleM{UserID: trimmedUserID, RoleID: roleID})
		}
		return tx.Create(&batch).Error
	})
}

func (rs *rbacStore) ListRoleIDsByUser(ctx context.Context, userID string) ([]string, error) {
	rows := make([]string, 0)
	err := rs.s.DB(ctx).Model(&model.RBACUserRoleM{}).
		Where("user_id = ?", strings.TrimSpace(userID)).
		Order("role_id ASC").
		Pluck("role_id", &rows).Error
	if err != nil {
		return nil, err
	}
	return uniqueNonEmpty(rows), nil
}

func (rs *rbacStore) ReplaceRolePermissions(ctx context.Context, roleID string, permissionIDs []string) error {
	trimmedRoleID := strings.TrimSpace(roleID)
	if trimmedRoleID == "" {
		return nil
	}
	cleanPermissionIDs := uniqueNonEmpty(permissionIDs)
	return rs.s.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", trimmedRoleID).Delete(&model.RBACRolePermissionM{}).Error; err != nil {
			return err
		}
		if len(cleanPermissionIDs) == 0 {
			return nil
		}
		batch := make([]*model.RBACRolePermissionM, 0, len(cleanPermissionIDs))
		for _, permissionID := range cleanPermissionIDs {
			batch = append(batch, &model.RBACRolePermissionM{RoleID: trimmedRoleID, PermissionID: permissionID})
		}
		return tx.Create(&batch).Error
	})
}

func (rs *rbacStore) ListPermissionsByRole(ctx context.Context, roleID string) ([]*model.RBACPermissionM, error) {
	out := make([]*model.RBACPermissionM, 0)
	err := rs.s.DB(ctx).
		Table(model.TableNameRBACRolePermissionM+" AS rp").
		Select("p.*").
		Joins("JOIN "+model.TableNameRBACPermissionM+" AS p ON p.permission_id = rp.permission_id").
		Where("rp.role_id = ?", strings.TrimSpace(roleID)).
		Order("p.permission_id ASC").
		Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []*model.RBACPermissionM{}, nil
	}
	return out, nil
}

func (rs *rbacStore) ListPolicyRows(ctx context.Context) ([]*RBACPolicyRow, error) {
	rows := make([]*RBACPolicyRow, 0)
	err := rs.s.DB(ctx).
		Table(model.TableNameRBACRolePermissionM+" AS rp").
		Select("rp.role_id AS role_id, p.resource AS resource, p.action AS action").
		Joins("JOIN "+model.TableNameRBACPermissionM+" AS p ON p.permission_id = rp.permission_id").
		Joins("JOIN "+model.TableNameRBACRoleM+" AS r ON r.role_id = rp.role_id").
		Where("p.status = ? AND r.status = ?", rbacStatusActive, rbacStatusActive).
		Order("rp.role_id ASC, p.permission_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []*RBACPolicyRow{}, nil
	}
	return rows, nil
}

func (rs *rbacStore) ListGroupingRows(ctx context.Context) ([]*RBACGroupingRow, error) {
	rows := make([]*RBACGroupingRow, 0)
	err := rs.s.DB(ctx).
		Table(model.TableNameRBACUserRoleM+" AS ur").
		Select("ur.user_id AS user_id, ur.role_id AS role_id").
		Joins("JOIN "+model.TableNameRBACUserM+" AS u ON u.user_id = ur.user_id").
		Joins("JOIN "+model.TableNameRBACRoleM+" AS r ON r.role_id = ur.role_id").
		Where("u.status = ? AND r.status = ?", rbacStatusActive, rbacStatusActive).
		Order("ur.user_id ASC, ur.role_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []*RBACGroupingRow{}, nil
	}
	return rows, nil
}

func normalizeOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return rbacDefaultListLimit
	}
	if limit > rbacMaxListLimit {
		return rbacMaxListLimit
	}
	return limit
}

func uniqueNonEmpty(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(input))
	out := make([]string, 0, len(input))
	for _, item := range input {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
