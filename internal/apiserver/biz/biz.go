package biz

import (
	"github.com/google/wire"
	datasourcev1 "zk8s.com/rca-api/internal/apiserver/biz/v1/datasource"
	evidencev1 "zk8s.com/rca-api/internal/apiserver/biz/v1/evidence"
	incidentv1 "zk8s.com/rca-api/internal/apiserver/biz/v1/incident"

	"zk8s.com/rca-api/internal/apiserver/store"
)

// ProviderSet declares dependency injection rules for the business logic layer.
var ProviderSet = wire.NewSet(NewBiz, wire.Bind(new(IBiz), new(*biz)))

// IBiz defines the access points for various business logic modules.
type IBiz interface {
	IncidentV1() incidentv1.IncidentBiz
	DatasourceV1() datasourcev1.DatasourceBiz
	EvidenceV1() evidencev1.EvidenceBiz
}

// biz is the concrete implementation of the business logic IBiz.
type biz struct {
	store store.IStore
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

func (b *biz) DatasourceV1() datasourcev1.DatasourceBiz {
	return datasourcev1.New(b.store)
}

func (b *biz) EvidenceV1() evidencev1.EvidenceBiz {
	return evidencev1.New(b.store)
}
