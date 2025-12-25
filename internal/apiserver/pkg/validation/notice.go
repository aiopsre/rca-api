package validation

import (
	"context"
	"net/url"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"

	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

const (
	defaultNoticeListLimit = int64(20)
	maxNoticeListLimit     = int64(200)

	noticeTimeoutMin = int64(500)
	noticeTimeoutMax = int64(10000)

	maxNoticeNameLength     = 128
	maxNoticeURLLength      = 2048
	maxNoticeSecretLength   = 4096
	maxNoticeHeaderKeyLen   = 256
	maxNoticeHeaderValueLen = 4096
	maxNoticeEventTypeLen   = 64
	maxNoticeStatusLen      = 16
	maxNoticeResourceIDLen  = 64
	maxNoticeRetries        = int64(10)
)

//nolint:gocognit,gocyclo // Guardrails are explicit by design for auditability.
func (v *Validator) ValidateCreateNoticeChannelRequest(ctx context.Context, rq *v1.CreateNoticeChannelRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if len(strings.TrimSpace(rq.GetName())) == 0 || len(strings.TrimSpace(rq.GetName())) > maxNoticeNameLength {
		return errorsx.ErrInvalidArgument
	}
	if rq.Type != nil {
		if strings.ToLower(strings.TrimSpace(rq.GetType())) != "webhook" {
			return errorsx.ErrInvalidArgument
		}
	}
	if rq.Secret != nil && len(strings.TrimSpace(rq.GetSecret())) > maxNoticeSecretLength {
		return errorsx.ErrInvalidArgument
	}
	if err := validateEndpointURL(strings.TrimSpace(rq.GetEndpointURL())); err != nil {
		return err
	}
	if err := validateHeaders(rq.GetHeaders()); err != nil {
		return err
	}
	if rq.TimeoutMs != nil {
		clamped := clampNoticeTimeoutMs(rq.GetTimeoutMs())
		rq.TimeoutMs = &clamped
	}
	if rq.MaxRetries != nil {
		if rq.GetMaxRetries() < 0 || rq.GetMaxRetries() > maxNoticeRetries {
			return errorsx.ErrInvalidArgument
		}
	}
	return nil
}

func (v *Validator) ValidateGetNoticeChannelRequest(ctx context.Context, rq *v1.GetNoticeChannelRequest) error {
	_ = ctx
	if rq == nil || !isValidResourceID(rq.GetChannelID()) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListNoticeChannelsRequest(ctx context.Context, rq *v1.ListNoticeChannelsRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultNoticeListLimit
	}
	if rq.GetLimit() > maxNoticeListLimit {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

//nolint:gocognit,gocyclo // Guardrails are explicit by design for auditability.
func (v *Validator) ValidatePatchNoticeChannelRequest(ctx context.Context, rq *v1.PatchNoticeChannelRequest) error {
	_ = ctx
	if rq == nil || !isValidResourceID(rq.GetChannelID()) {
		return errorsx.ErrInvalidArgument
	}
	if rq.Enabled == nil && rq.EndpointURL == nil && rq.Headers == nil && rq.TimeoutMs == nil && rq.MaxRetries == nil && rq.Secret == nil {
		return errorsx.ErrInvalidArgument
	}
	if rq.EndpointURL != nil {
		if err := validateEndpointURL(strings.TrimSpace(rq.GetEndpointURL())); err != nil {
			return err
		}
	}
	if rq.Headers != nil {
		if err := validateHeaders(rq.GetHeaders()); err != nil {
			return err
		}
	}
	if rq.TimeoutMs != nil {
		clamped := clampNoticeTimeoutMs(rq.GetTimeoutMs())
		rq.TimeoutMs = &clamped
	}
	if rq.MaxRetries != nil {
		if rq.GetMaxRetries() < 0 || rq.GetMaxRetries() > maxNoticeRetries {
			return errorsx.ErrInvalidArgument
		}
	}
	if rq.Secret != nil && len(strings.TrimSpace(rq.GetSecret())) > maxNoticeSecretLength {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateDeleteNoticeChannelRequest(ctx context.Context, rq *v1.DeleteNoticeChannelRequest) error {
	_ = ctx
	if rq == nil || !isValidResourceID(rq.GetChannelID()) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateGetNoticeDeliveryRequest(ctx context.Context, rq *v1.GetNoticeDeliveryRequest) error {
	_ = ctx
	if rq == nil || !isValidResourceID(rq.GetDeliveryID()) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

//nolint:gocognit,gocyclo // Query guardrails are explicit by design for auditability.
func (v *Validator) ValidateListNoticeDeliveriesRequest(ctx context.Context, rq *v1.ListNoticeDeliveriesRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultNoticeListLimit
	}
	if rq.GetLimit() > maxNoticeListLimit {
		return errorsx.ErrInvalidArgument
	}
	if rq.ChannelID != nil && !isValidResourceID(rq.GetChannelID()) {
		return errorsx.ErrInvalidArgument
	}
	if rq.IncidentID != nil && !isValidResourceID(rq.GetIncidentID()) {
		return errorsx.ErrInvalidArgument
	}
	if rq.EventType != nil {
		eventType := strings.ToLower(strings.TrimSpace(rq.GetEventType()))
		if eventType == "" || len(eventType) > maxNoticeEventTypeLen {
			return errorsx.ErrInvalidArgument
		}
		rq.EventType = &eventType
	}
	if rq.Status != nil {
		status := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
		if status == "" || len(status) > maxNoticeStatusLen {
			return errorsx.ErrInvalidArgument
		}
		rq.Status = &status
	}
	return nil
}

func clampNoticeTimeoutMs(in int64) int64 {
	switch {
	case in < noticeTimeoutMin:
		return noticeTimeoutMin
	case in > noticeTimeoutMax:
		return noticeTimeoutMax
	default:
		return in
	}
}

func validateEndpointURL(endpoint string) error {
	if endpoint == "" || len(endpoint) > maxNoticeURLLength {
		return errorsx.ErrInvalidArgument
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil {
		return errorsx.ErrInvalidArgument
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if (scheme != "http" && scheme != "https") || strings.TrimSpace(parsed.Host) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func validateHeaders(headers map[string]string) error {
	for key, value := range headers {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" || len(trimmedKey) > maxNoticeHeaderKeyLen {
			return errorsx.ErrInvalidArgument
		}
		if len(value) > maxNoticeHeaderValueLen {
			return errorsx.ErrInvalidArgument
		}
	}
	return nil
}

func isValidResourceID(v string) bool {
	trimmed := strings.TrimSpace(v)
	return trimmed != "" && len(trimmed) <= maxNoticeResourceIDLen
}
