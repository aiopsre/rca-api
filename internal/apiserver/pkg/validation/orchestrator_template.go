package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func (v *Validator) ValidateRegisterOrchestratorTemplatesRequest(
	ctx context.Context,
	req *v1.RegisterOrchestratorTemplatesRequest,
) error {
	_ = ctx
	if req == nil {
		return errorsx.ErrInvalidArgument
	}

	req.InstanceID = strings.TrimSpace(req.GetInstanceID())
	if req.GetInstanceID() == "" {
		return errorsx.ErrInvalidArgument
	}

	templates := req.GetTemplates()
	if len(templates) == 0 {
		return errorsx.ErrInvalidArgument
	}
	for _, item := range templates {
		if item == nil {
			return errorsx.ErrInvalidArgument
		}
		item.TemplateID = strings.TrimSpace(item.GetTemplateID())
		item.Version = strings.TrimSpace(item.GetVersion())
		if item.GetTemplateID() == "" {
			return errorsx.ErrInvalidArgument
		}
	}

	return nil
}

func (v *Validator) ValidateListOrchestratorTemplatesRequest(
	ctx context.Context,
	req *v1.ListOrchestratorTemplatesRequest,
) error {
	_ = ctx
	if req == nil {
		return errorsx.ErrInvalidArgument
	}
	return nil
}
