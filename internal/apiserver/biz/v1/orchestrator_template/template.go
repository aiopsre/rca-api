package orchestrator_template

//go:generate mockgen -destination mock_template.go -package orchestrator_template github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_template TemplateBiz

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorregistry"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

// TemplateBiz defines orchestrator template registry use-cases.
type TemplateBiz interface {
	Register(ctx context.Context, req *v1.RegisterOrchestratorTemplatesRequest) (*v1.RegisterOrchestratorTemplatesResponse, error)
	List(ctx context.Context, req *v1.ListOrchestratorTemplatesRequest) (*v1.ListOrchestratorTemplatesResponse, error)

	TemplateExpansion
}

//nolint:modernize // Keep explicit placeholder for future extensions.
type TemplateExpansion interface{}

type templateBiz struct{}

var _ TemplateBiz = (*templateBiz)(nil)

// New creates orchestrator template biz.
func New() *templateBiz {
	return &templateBiz{}
}

func (b *templateBiz) Register(
	ctx context.Context,
	req *v1.RegisterOrchestratorTemplatesRequest,
) (*v1.RegisterOrchestratorTemplatesResponse, error) {
	if req == nil {
		return nil, errno.ErrInvalidArgument
	}
	if err := orchestratorregistry.Register(ctx, req.GetInstanceID(), req.GetTemplates()); err != nil {
		return nil, errno.ErrOrchestratorTemplateRegisterFailed
	}
	return &v1.RegisterOrchestratorTemplatesResponse{Count: int32(len(req.GetTemplates()))}, nil
}

func (b *templateBiz) List(
	ctx context.Context,
	req *v1.ListOrchestratorTemplatesRequest,
) (*v1.ListOrchestratorTemplatesResponse, error) {
	if req == nil {
		return nil, errno.ErrInvalidArgument
	}
	entries, err := orchestratorregistry.List(ctx)
	if err != nil {
		return nil, errno.ErrOrchestratorTemplateListFailed
	}
	return &v1.ListOrchestratorTemplatesResponse{Templates: entries}, nil
}
