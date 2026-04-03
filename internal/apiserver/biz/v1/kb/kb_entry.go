package kb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultKBEntryListLimit = int64(20)
	maxKBEntryListLimit     = int64(200)
)

// KBEntryBiz defines knowledge base entry management use-cases.
type KBEntryBiz interface {
	Create(ctx context.Context, rq *v1.CreateKBEntryRequest) (*v1.CreateKBEntryResponse, error)
	Get(ctx context.Context, rq *v1.GetKBEntryRequest) (*v1.GetKBEntryResponse, error)
	List(ctx context.Context, rq *v1.ListKBEntriesRequest) (*v1.ListKBEntriesResponse, error)
	Update(ctx context.Context, rq *v1.UpdateKBEntryRequest) (*v1.UpdateKBEntryResponse, error)
	Delete(ctx context.Context, rq *v1.DeleteKBEntryRequest) (*v1.DeleteKBEntryResponse, error)

	KBEntryExpansion
}

// KBEntryExpansion defines additional methods for KB entry.
type KBEntryExpansion interface{}

// kbEntryBiz is the concrete implementation of KBEntryBiz.
type kbEntryBiz struct {
	store store.IStore
}

var _ KBEntryBiz = (*kbEntryBiz)(nil)

// NewKBEntry creates a new KB entry biz instance.
func NewKBEntry(store store.IStore) *kbEntryBiz {
	return &kbEntryBiz{store: store}
}

func (b *kbEntryBiz) Create(ctx context.Context, rq *v1.CreateKBEntryRequest) (*v1.CreateKBEntryResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}

	namespace := strings.TrimSpace(rq.GetNamespace())
	service := strings.TrimSpace(rq.GetService())
	rootCauseType := strings.TrimSpace(rq.GetRootCauseType())
	rootCauseSummary := sanitizeText(rq.GetRootCauseSummary(), maxRootCauseSummaryLen)
	patternsJSON := strings.TrimSpace(rq.GetPatternsJSON())

	if namespace == "" || service == "" || rootCauseType == "" || rootCauseSummary == "" || patternsJSON == "" {
		return nil, errno.ErrInvalidArgument
	}

	// Validate patterns JSON is valid JSON
	var patterns []Pattern
	if err := json.Unmarshal([]byte(patternsJSON), &patterns); err != nil {
		return nil, errno.ErrInvalidArgument
	}

	// Calculate patterns hash if not provided
	patternsHash := strings.TrimSpace(rq.GetPatternsHash())
	if patternsHash == "" {
		sum := sha256.Sum256([]byte(patternsJSON))
		patternsHash = hex.EncodeToString(sum[:])
	}

	// Generate KB ID if not provided
	kbID := strings.TrimSpace(rq.GetKbID())
	if kbID == "" {
		kbID = "kb-" + uuid.New().String()[:8]
	}

	// Check if KB ID already exists
	exists, err := b.store.KBEntry().Get(ctx, where.T(ctx).F("kb_id", kbID))
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrInternal
	}
	if exists != nil {
		return nil, errno.ErrKBEntryAlreadyExists
	}

	confidence := rq.GetConfidence()
	if confidence <= 0 {
		confidence = defaultWritebackConfidence
	}

	obj := &model.KBEntryM{
		KBID:             kbID,
		Namespace:        sanitizeScope(namespace, 128),
		Service:          sanitizeScope(service, 256),
		RootCauseType:    normalizeRootCauseType(rootCauseType),
		RootCauseSummary: rootCauseSummary,
		PatternsJSON:     patternsJSON,
		PatternsHash:     patternsHash,
		Confidence:       normalizeConfidence(confidence),
	}

	if evSig := rq.GetEvidenceSignatureJSON(); evSig != "" {
		evSig = strings.TrimSpace(evSig)
		if len(evSig) <= maxEvidenceSignatureBytes {
			obj.EvidenceSignatureJSON = &evSig
		}
	}

	if err := b.store.KBEntry().Create(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}

	return &v1.CreateKBEntryResponse{
		KbEntry: modelToProtoKBEntry(obj),
	}, nil
}

func (b *kbEntryBiz) Get(ctx context.Context, rq *v1.GetKBEntryRequest) (*v1.GetKBEntryResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}

	kbID := strings.TrimSpace(rq.GetKbID())
	if kbID == "" {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.KBEntry().Get(ctx, where.T(ctx).F("kb_id", kbID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}

	return &v1.GetKBEntryResponse{
		KbEntry: modelToProtoKBEntry(obj),
	}, nil
}

func (b *kbEntryBiz) List(ctx context.Context, rq *v1.ListKBEntriesRequest) (*v1.ListKBEntriesResponse, error) {
	if rq == nil {
		rq = &v1.ListKBEntriesRequest{}
	}

	limit := rq.GetLimit()
	if limit <= 0 {
		limit = defaultKBEntryListLimit
	}
	if limit > maxKBEntryListLimit {
		limit = maxKBEntryListLimit
	}

	opts := where.T(ctx).P(int(rq.GetOffset()), int(limit))

	if ns := strings.TrimSpace(rq.GetNamespace()); ns != "" {
		opts = opts.F("namespace", ns)
	}
	if svc := strings.TrimSpace(rq.GetService()); svc != "" {
		opts = opts.F("service", svc)
	}
	if rct := strings.TrimSpace(rq.GetRootCauseType()); rct != "" {
		opts = opts.F("root_cause_type", rct)
	}

	total, entries, err := b.store.KBEntry().List(ctx, opts)
	if err != nil {
		return nil, errno.ErrInternal
	}

	items := make([]*v1.KBEntry, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		items = append(items, modelToProtoKBEntry(entry))
	}

	return &v1.ListKBEntriesResponse{
		TotalCount: total,
		KbEntries:  items,
	}, nil
}

func (b *kbEntryBiz) Update(ctx context.Context, rq *v1.UpdateKBEntryRequest) (*v1.UpdateKBEntryResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}

	kbID := strings.TrimSpace(rq.GetKbID())
	if kbID == "" {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.KBEntry().Get(ctx, where.T(ctx).F("kb_id", kbID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}

	updated := false

	if rq.RootCauseSummary != nil {
		obj.RootCauseSummary = sanitizeText(*rq.RootCauseSummary, maxRootCauseSummaryLen)
		updated = true
	}
	if rq.PatternsJSON != nil {
		// Validate JSON
		var patterns []Pattern
		if err := json.Unmarshal([]byte(*rq.PatternsJSON), &patterns); err != nil {
			return nil, errno.ErrInvalidArgument
		}
		obj.PatternsJSON = *rq.PatternsJSON
		updated = true

		// Update hash if patterns changed
		if rq.PatternsHash != nil {
			obj.PatternsHash = *rq.PatternsHash
		} else {
			sum := sha256.Sum256([]byte(*rq.PatternsJSON))
			obj.PatternsHash = hex.EncodeToString(sum[:])
		}
	}
	if rq.EvidenceSignatureJSON != nil {
		evSigStr := strings.TrimSpace(*rq.EvidenceSignatureJSON)
		if len(evSigStr) <= maxEvidenceSignatureBytes {
			obj.EvidenceSignatureJSON = &evSigStr
		}
		updated = true
	}
	if rq.Confidence != nil {
		obj.Confidence = normalizeConfidence(*rq.Confidence)
		updated = true
	}

	if !updated {
		return &v1.UpdateKBEntryResponse{
			KbEntry: modelToProtoKBEntry(obj),
		}, nil
	}

	if err := b.store.KBEntry().Update(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}

	return &v1.UpdateKBEntryResponse{
		KbEntry: modelToProtoKBEntry(obj),
	}, nil
}

func (b *kbEntryBiz) Delete(ctx context.Context, rq *v1.DeleteKBEntryRequest) (*v1.DeleteKBEntryResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}

	kbID := strings.TrimSpace(rq.GetKbID())
	if kbID == "" {
		return nil, errno.ErrInvalidArgument
	}

	if err := b.store.KBEntry().Delete(ctx, where.T(ctx).F("kb_id", kbID)); err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}

	return &v1.DeleteKBEntryResponse{}, nil
}

func modelToProtoKBEntry(obj *model.KBEntryM) *v1.KBEntry {
	if obj == nil {
		return nil
	}

	entry := &v1.KBEntry{
		Id:               obj.ID,
		KbID:             strings.TrimSpace(obj.KBID),
		Namespace:        strings.TrimSpace(obj.Namespace),
		Service:          strings.TrimSpace(obj.Service),
		RootCauseType:    strings.TrimSpace(obj.RootCauseType),
		RootCauseSummary: strings.TrimSpace(obj.RootCauseSummary),
		PatternsJSON:     strings.TrimSpace(obj.PatternsJSON),
		PatternsHash:     strings.TrimSpace(obj.PatternsHash),
		Confidence:       obj.Confidence,
		HitCount:         obj.HitCount,
		CreatedAt:        timestamppb.New(obj.CreatedAt.UTC()),
		UpdatedAt:        timestamppb.New(obj.UpdatedAt.UTC()),
	}

	if obj.EvidenceSignatureJSON != nil {
		entry.EvidenceSignatureJSON = obj.EvidenceSignatureJSON
	}

	if obj.LastHitAt != nil {
		entry.LastHitAt = timestamppb.New(obj.LastHitAt.UTC())
	}

	return entry
}