package validation

import (
	"context"
	"net/url"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	defaultDatasourceTimeoutMs = int64(5000)
	maxDatasourceTimeoutMs     = int64(120000)
	defaultListLimit           = int64(20)
	maxListLimit               = int64(200)
)

var (
	allowedDatasourceTypes = map[string]struct{}{
		"prometheus":    {},
		"loki":          {},
		"elasticsearch": {},
		"tempo":         {},
	}
	allowedAuthTypes = map[string]struct{}{
		"none":    {},
		"basic":   {},
		"bearer":  {},
		"api_key": {},
	}
)

func (v *Validator) ValidateCreateDatasourceRequest(ctx context.Context, rq *v1.CreateDatasourceRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetName()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if err := validateDatasourceType(rq.GetType()); err != nil {
		return err
	}
	if err := validateBaseURL(rq.GetBaseURL()); err != nil {
		return err
	}
	if rq.AuthType != nil {
		if err := validateAuthType(rq.GetAuthType()); err != nil {
			return err
		}
	}
	if rq.TimeoutMs != nil && (rq.GetTimeoutMs() <= 0 || rq.GetTimeoutMs() > maxDatasourceTimeoutMs) {
		return errorsx.ErrInvalidArgument
	}
	if rq.AuthType == nil || strings.TrimSpace(rq.GetAuthType()) == "" {
		rq.AuthType = ptrString("none")
	}
	if rq.TimeoutMs == nil || rq.GetTimeoutMs() <= 0 {
		rq.TimeoutMs = ptrInt64(defaultDatasourceTimeoutMs)
	}
	return genericvalidation.ValidateAllFields(rq, genericvalidation.Rules{})
}

func (v *Validator) ValidateUpdateDatasourceRequest(ctx context.Context, rq *v1.UpdateDatasourceRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetDatasourceID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.Name != nil && strings.TrimSpace(rq.GetName()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.BaseURL != nil {
		if err := validateBaseURL(rq.GetBaseURL()); err != nil {
			return err
		}
	}
	if rq.AuthType != nil {
		if err := validateAuthType(rq.GetAuthType()); err != nil {
			return err
		}
	}
	if rq.TimeoutMs != nil && (rq.GetTimeoutMs() <= 0 || rq.GetTimeoutMs() > maxDatasourceTimeoutMs) {
		return errorsx.ErrInvalidArgument
	}
	return genericvalidation.ValidateAllFields(rq, genericvalidation.Rules{})
}

func (v *Validator) ValidateDeleteDatasourceRequest(ctx context.Context, rq *v1.DeleteDatasourceRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetDatasourceID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateGetDatasourceRequest(ctx context.Context, rq *v1.GetDatasourceRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetDatasourceID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListDatasourceRequest(ctx context.Context, rq *v1.ListDatasourceRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultListLimit
	}
	if rq.GetLimit() > maxListLimit {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.Type != nil && strings.TrimSpace(rq.GetType()) != "" {
		if err := validateDatasourceType(rq.GetType()); err != nil {
			return err
		}
	}
	return nil
}

func validateDatasourceType(value string) error {
	typ := strings.ToLower(strings.TrimSpace(value))
	if _, ok := allowedDatasourceTypes[typ]; !ok {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func validateAuthType(value string) error {
	typ := strings.ToLower(strings.TrimSpace(value))
	if _, ok := allowedAuthTypes[typ]; !ok {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errorsx.ErrInvalidArgument
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func ptrString(v string) *string { return &v }
func ptrInt64(v int64) *int64    { return &v }
