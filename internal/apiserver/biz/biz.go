package biz

import (
	"github.com/google/wire"
	incidentv1 "zk8s.com/rca-api/internal/apiserver/biz/v1/incident"

	"zk8s.com/rca-api/internal/apiserver/store"
)

// ProviderSet declares dependency injection rules for the business logic layer.
var ProviderSet = wire.NewSet(NewBiz, wire.Bind(new(IBiz), new(*biz)))

// IBiz defines the access points for various business logic modules.
type IBiz interface{ IncidentV1() incidentv1.IncidentBiz }

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
