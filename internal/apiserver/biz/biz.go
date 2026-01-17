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

	incidentOnce   sync.Once
	incidentBiz    incidentv1.IncidentBiz
	alertEventOnce sync.Once
	alertEventBiz  alerteventv1.AlertEventBiz
	datasourceOnce sync.Once
	datasourceBiz  datasourcev1.DatasourceBiz
	evidenceOnce   sync.Once
	evidenceBiz    evidencev1.EvidenceBiz
	aiJobOnce      sync.Once
	aiJobBiz       aijobv1.AIJobBiz
	silenceOnce    sync.Once
	silenceBiz     silencev1.SilenceBiz
	noticeOnce     sync.Once
	noticeBiz      noticev1.NoticeBiz

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
	b.incidentOnce.Do(func() {
		b.incidentBiz = incidentv1.New(b.store)
	})
	return b.incidentBiz
}

func (b *biz) AlertEventV1() alerteventv1.AlertEventBiz {
	b.alertEventOnce.Do(func() {
		b.alertEventBiz = alerteventv1.New(b.store)
	})
	return b.alertEventBiz
}

func (b *biz) DatasourceV1() datasourcev1.DatasourceBiz {
	b.datasourceOnce.Do(func() {
		b.datasourceBiz = datasourcev1.New(b.store)
	})
	return b.datasourceBiz
}

func (b *biz) EvidenceV1() evidencev1.EvidenceBiz {
	b.evidenceOnce.Do(func() {
		b.evidenceBiz = evidencev1.New(b.store)
	})
	return b.evidenceBiz
}

func (b *biz) AIJobV1() aijobv1.AIJobBiz {
	b.aiJobOnce.Do(func() {
		b.aiJobBiz = aijobv1.New(b.store)
	})
	return b.aiJobBiz
}

func (b *biz) SilenceV1() silencev1.SilenceBiz {
	b.silenceOnce.Do(func() {
		b.silenceBiz = silencev1.New(b.store)
	})
	return b.silenceBiz
}

func (b *biz) NoticeV1() noticev1.NoticeBiz {
	b.noticeOnce.Do(func() {
		b.noticeBiz = noticev1.New(b.store)
	})
	return b.noticeBiz
}

func (b *biz) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		var errs []error
		errs = appendCloseIfSupported(errs, b.incidentBiz)
		errs = appendCloseIfSupported(errs, b.alertEventBiz)
		errs = appendCloseIfSupported(errs, b.datasourceBiz)
		errs = appendCloseIfSupported(errs, b.evidenceBiz)
		errs = appendCloseIfSupported(errs, b.aiJobBiz)
		errs = appendCloseIfSupported(errs, b.silenceBiz)
		errs = appendCloseIfSupported(errs, b.noticeBiz)
		b.closeErr = errors.Join(errs...)
	})
	return b.closeErr
}

func appendCloseIfSupported(errs []error, target any) []error {
	if target == nil {
		return errs
	}
	closer, ok := target.(interface{ Close() error })
	if !ok {
		return errs
	}
	return append(errs, closer.Close())
}
