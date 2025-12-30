package validation

import (
	"context"
	"net/url"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"

	noticepkg "zk8s.com/rca-api/internal/apiserver/pkg/notice"
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
	maxNoticeSummaryTPLLen  = 512
	maxNoticeHeaderKeyLen   = 256
	maxNoticeHeaderValueLen = 4096
	maxNoticeEventTypeLen   = 64
	maxNoticeStatusLen      = 16
	maxNoticeResourceIDLen  = 64
	maxNoticeRetries        = int64(20)
	maxNoticeSelectorItems  = 100
	maxNoticeSelectorLength = 128
)

var allowedNoticeDeliveryStatus = map[string]struct{}{
	"pending":   {},
	"succeeded": {},
	"failed":    {},
	"canceled":  {},
}

var allowedNoticeSelectorEvents = map[string]struct{}{
	noticepkg.EventTypeIncidentCreated:  {},
	noticepkg.EventTypeDiagnosisWritten: {},
}

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
	if !isAllowedNoticePayloadMode(rq.GetPayloadMode(), true) {
		return errorsx.ErrInvalidArgument
	}
	if rq.BaseURL != nil {
		baseURL := strings.TrimSpace(rq.GetBaseURL())
		if baseURL != "" {
			if err := validateEndpointURL(baseURL); err != nil {
				return err
			}
		}
		rq.BaseURL = &baseURL
	}
	if rq.SummaryTemplate != nil {
		tpl := strings.TrimSpace(rq.GetSummaryTemplate())
		if len(tpl) > maxNoticeSummaryTPLLen {
			return errorsx.ErrInvalidArgument
		}
		rq.SummaryTemplate = &tpl
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
	if err := validateNoticeSelectors(rq.GetSelectors()); err != nil {
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
	patchEmpty := rq.Enabled == nil &&
		rq.EndpointURL == nil &&
		rq.Headers == nil &&
		rq.TimeoutMs == nil &&
		rq.MaxRetries == nil &&
		rq.Secret == nil &&
		rq.GetSelectors() == nil &&
		rq.PayloadMode == nil &&
		rq.IncludeDiagnosis == nil &&
		rq.IncludeEvidenceIds == nil &&
		rq.IncludeRootCause == nil &&
		rq.IncludeLinks == nil &&
		rq.BaseURL == nil &&
		rq.SummaryTemplate == nil
	if patchEmpty {
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
	if rq.PayloadMode != nil && !isAllowedNoticePayloadMode(rq.GetPayloadMode(), false) {
		return errorsx.ErrInvalidArgument
	}
	if rq.BaseURL != nil {
		baseURL := strings.TrimSpace(rq.GetBaseURL())
		if baseURL != "" {
			if err := validateEndpointURL(baseURL); err != nil {
				return err
			}
		}
		rq.BaseURL = &baseURL
	}
	if rq.SummaryTemplate != nil {
		tpl := strings.TrimSpace(rq.GetSummaryTemplate())
		if len(tpl) > maxNoticeSummaryTPLLen {
			return errorsx.ErrInvalidArgument
		}
		rq.SummaryTemplate = &tpl
	}
	if rq.GetSelectors() != nil {
		if err := validateNoticeSelectors(rq.GetSelectors()); err != nil {
			return err
		}
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

func (v *Validator) ValidateReplayNoticeDeliveryRequest(ctx context.Context, rq *v1.ReplayNoticeDeliveryRequest) error {
	_ = ctx
	if rq == nil || !isValidResourceID(rq.GetDeliveryID()) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateCancelNoticeDeliveryRequest(ctx context.Context, rq *v1.CancelNoticeDeliveryRequest) error {
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
		if _, ok := allowedNoticeDeliveryStatus[status]; !ok {
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

func isAllowedNoticePayloadMode(mode v1.NoticePayloadMode, allowUnspecified bool) bool {
	switch mode {
	case v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_COMPACT, v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_FULL:
		return true
	case v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_UNSPECIFIED:
		return allowUnspecified
	default:
		return false
	}
}

func isValidResourceID(v string) bool {
	trimmed := strings.TrimSpace(v)
	return trimmed != "" && len(trimmed) <= maxNoticeResourceIDLen
}

func validateNoticeSelectors(selectors *v1.NoticeSelectors) error {
	if selectors == nil {
		return nil
	}

	eventTypes, err := normalizeSelectorList(selectors.GetEventTypes(), normalizeSelectorEventType)
	if err != nil {
		return err
	}
	selectors.EventTypes = eventTypes

	namespaces, err := normalizeSelectorList(selectors.GetNamespaces(), normalizeSelectorIdentity)
	if err != nil {
		return err
	}
	selectors.Namespaces = namespaces

	services, err := normalizeSelectorList(selectors.GetServices(), normalizeSelectorIdentity)
	if err != nil {
		return err
	}
	selectors.Services = services

	severities, err := normalizeSelectorList(selectors.GetSeverities(), normalizeSelectorSeverity)
	if err != nil {
		return err
	}
	selectors.Severities = severities

	rootCauseTypes, err := normalizeSelectorList(selectors.GetRootCauseTypes(), normalizeSelectorIdentity)
	if err != nil {
		return err
	}
	selectors.RootCauseTypes = rootCauseTypes

	return nil
}

func normalizeSelectorList(items []string, normalize func(string) (string, bool)) ([]string, error) {
	if len(items) > maxNoticeSelectorItems {
		return nil, errorsx.ErrInvalidArgument
	}
	if len(items) == 0 {
		return nil, nil
	}

	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		v, ok := normalize(item)
		if !ok {
			return nil, errorsx.ErrInvalidArgument
		}
		if _, exists := seen[v]; exists {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out, nil
}

func normalizeSelectorEventType(raw string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || len(v) > maxNoticeSelectorLength {
		return "", false
	}
	if _, ok := allowedNoticeSelectorEvents[v]; !ok {
		return "", false
	}
	return v, true
}

func normalizeSelectorIdentity(raw string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || len(v) > maxNoticeSelectorLength {
		return "", false
	}
	return v, true
}

func normalizeSelectorSeverity(raw string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || len(v) > maxNoticeSelectorLength {
		return "", false
	}
	switch v {
	case "critical", "p0", "high":
		return "critical", true
	case "warning", "warn", "p1", "medium":
		return "warning", true
	case "info", "p2", "p3", "low":
		return "info", true
	default:
		return "", false
	}
}
