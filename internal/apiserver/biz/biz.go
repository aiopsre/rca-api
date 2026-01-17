package biz

import (
	"errors"
	"sync"

	aijobv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	alerteventv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/alert_event"
	datasourcev1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/datasource"
	evidencev1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/evidence"
	incidentv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/incident"
	noticev1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/notice"
	silencev1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/silence"
	"github.com/google/wire"

	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

// ProviderSet declares dependency injection rules for the business logic layer.
var ProviderSet = wire.NewSet(NewBiz, wire.Bind(new(IBiz), new(*biz)))

// IBiz defines the access points for various business logic modules.
//
//nolint:interfacebloat // Aggregation interface intentionally exposes all domain biz modules.
type IBiz interface {
	IncidentV1() incidentv1.IncidentBiz
	AlertEventV1() alerteventv1.AlertEventBiz
	DatasourceV1() datasourcev1.DatasourceBiz
	EvidenceV1() evidencev1.EvidenceBiz
	AIJobV1() aijobv1.AIJobBiz
	SilenceV1() silencev1.SilenceBiz
	NoticeV1() noticev1.NoticeBiz
	Close() error
}

// biz is the concrete implementation of the business logic IBiz.
type biz struct {
	store store.IStore

	alertEventOnce sync.Once
	alertEventBiz  alerteventv1.AlertEventBiz

	closeOnce sync.Once
	closeErr  error
}

// Ensure biz implements IBiz at compile time.
var _ IBiz = (*biz)(nil)

// NewBiz creates and returns a new instance of the business logic layer.
func NewBiz(store store.IStore) *biz {
	return &biz{store: store}
}

func (b *biz) IncidentV1() incidentv1.IncidentBiz {
	return incidentv1.New(b.store)
}

func (b *biz) AlertEventV1() alerteventv1.AlertEventBiz {
	b.alertEventOnce.Do(func() {
		b.alertEventBiz = alerteventv1.New(b.store)
	})
	return b.alertEventBiz
}

func (b *biz) DatasourceV1() datasourcev1.DatasourceBiz {
	return datasourcev1.New(b.store)
}

func (b *biz) EvidenceV1() evidencev1.EvidenceBiz {
	return evidencev1.New(b.store)
}

func (b *biz) AIJobV1() aijobv1.AIJobBiz {
	return aijobv1.New(b.store)
}

func (b *biz) SilenceV1() silencev1.SilenceBiz {
	return silencev1.New(b.store)
}

func (b *biz) NoticeV1() noticev1.NoticeBiz {
	return noticev1.New(b.store)
}

func (b *biz) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		var errs []error
		if b.alertEventBiz != nil {
			if closer, ok := b.alertEventBiz.(interface{ Close() error }); ok {
				errs = append(errs, closer.Close())
			}
		}
		b.closeErr = errors.Join(errs...)
	})
	return b.closeErr
}
