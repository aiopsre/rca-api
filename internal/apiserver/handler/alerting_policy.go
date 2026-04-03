package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

// CreateAlertingPolicy handles the HTTP request to create a new alerting policy.
func (h *Handler) CreateAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.AlertingPolicyV1().Create, h.val.ValidateCreateAlertingPolicyRequest)
}

// GetAlertingPolicy handles the HTTP request to get an alerting policy by ID.
func (h *Handler) GetAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.AlertingPolicyV1().Get, h.val.ValidateGetAlertingPolicyRequest)
}

// ListAlertingPolicies handles the HTTP request to list alerting policies.
func (h *Handler) ListAlertingPolicies(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.AlertingPolicyV1().List, h.val.ValidateListAlertingPoliciesRequest)
}

// UpdateAlertingPolicy handles the HTTP request to update an alerting policy.
func (h *Handler) UpdateAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.AlertingPolicyV1().Update, h.val.ValidateUpdateAlertingPolicyRequest)
}

// DeleteAlertingPolicy handles the HTTP request to delete an alerting policy.
func (h *Handler) DeleteAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.AlertingPolicyV1().Delete, h.val.ValidateDeleteAlertingPolicyRequest)
}

// ActivateAlertingPolicy handles the HTTP request to activate an alerting policy.
func (h *Handler) ActivateAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.AlertingPolicyV1().Activate, h.val.ValidateActivateAlertingPolicyRequest)
}

// DeactivateAlertingPolicy handles the HTTP request to deactivate an alerting policy.
func (h *Handler) DeactivateAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.AlertingPolicyV1().Deactivate, h.val.ValidateDeactivateAlertingPolicyRequest)
}

// RollbackAlertingPolicy handles the HTTP request to rollback an alerting policy.
func (h *Handler) RollbackAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.AlertingPolicyV1().Rollback, h.val.ValidateRollbackAlertingPolicyRequest)
}

// GetActiveAlertingPolicy handles the HTTP request to get the currently active alerting policy.
func (h *Handler) GetActiveAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.AlertingPolicyV1().GetActive, h.val.ValidateGetActiveAlertingPolicyRequest)
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		alertingPolicies := v1.Group("/alerting-policies", mws...)
		alertingPolicies.POST("", handler.CreateAlertingPolicy)
		alertingPolicies.GET("", handler.ListAlertingPolicies)
		alertingPolicies.GET("/active", handler.GetActiveAlertingPolicy)
		alertingPolicies.GET("/:id", handler.GetAlertingPolicy)
		alertingPolicies.PUT("/:id", handler.UpdateAlertingPolicy)
		alertingPolicies.DELETE("/:id", handler.DeleteAlertingPolicy)
		alertingPolicies.POST("/:id/activate", handler.ActivateAlertingPolicy)
		alertingPolicies.POST("/:id/deactivate", handler.DeactivateAlertingPolicy)
		alertingPolicies.POST("/:id/rollback", handler.RollbackAlertingPolicy)
	})
}