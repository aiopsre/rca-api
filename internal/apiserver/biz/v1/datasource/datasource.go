package datasource

//go:generate mockgen -destination mock_datasource.go -package datasource github.com/aiopsre/rca-api/internal/apiserver/biz/v1/datasource DatasourceBiz

import (
	"context"
	"strings"

	"github.com/jinzhu/copier"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/conversion"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

// DatasourceBiz defines datasource domain use-cases.
type DatasourceBiz interface {
	Create(ctx context.Context, rq *v1.CreateDatasourceRequest) (*v1.CreateDatasourceResponse, error)
	Update(ctx context.Context, rq *v1.UpdateDatasourceRequest) (*v1.UpdateDatasourceResponse, error)
	Delete(ctx context.Context, rq *v1.DeleteDatasourceRequest) (*v1.DeleteDatasourceResponse, error)
	Get(ctx context.Context, rq *v1.GetDatasourceRequest) (*v1.GetDatasourceResponse, error)
	List(ctx context.Context, rq *v1.ListDatasourceRequest) (*v1.ListDatasourceResponse, error)

	DatasourceExpansion
}

type DatasourceExpansion interface{}

type datasourceBiz struct {
	store store.IStore
}

var _ DatasourceBiz = (*datasourceBiz)(nil)

// New creates datasource biz.
func New(store store.IStore) *datasourceBiz {
	return &datasourceBiz{store: store}
}

func (b *datasourceBiz) Create(ctx context.Context, rq *v1.CreateDatasourceRequest) (*v1.CreateDatasourceResponse, error) {
	m := &model.DatasourceM{}
	_ = copier.Copy(m, rq)

	applyDatasourceDefaults(m)

	if err := b.store.Datasource().Create(ctx, m); err != nil {
		return nil, errno.ErrDatasourceCreateFailed
	}

	return &v1.CreateDatasourceResponse{DatasourceID: m.DatasourceID}, nil
}

func (b *datasourceBiz) Update(ctx context.Context, rq *v1.UpdateDatasourceRequest) (*v1.UpdateDatasourceResponse, error) {
	m, err := b.store.Datasource().Get(ctx, where.T(ctx).F("datasource_id", rq.GetDatasourceID()))
	if err != nil {
		return nil, toDatasourceGetError(err)
	}

	if rq.Name != nil {
		m.Name = strings.TrimSpace(rq.GetName())
	}
	if rq.BaseURL != nil {
		m.BaseURL = strings.TrimSpace(rq.GetBaseURL())
	}
	if rq.AuthType != nil {
		m.AuthType = strings.ToLower(strings.TrimSpace(rq.GetAuthType()))
	}
	if rq.AuthSecretRef != nil {
		v := strings.TrimSpace(rq.GetAuthSecretRef())
		m.AuthSecretRef = &v
	}
	if rq.TimeoutMs != nil {
		m.TimeoutMs = rq.GetTimeoutMs()
	}
	if rq.IsEnabled != nil {
		m.IsEnabled = rq.GetIsEnabled()
	}
	if rq.LabelsJSON != nil {
		v := strings.TrimSpace(rq.GetLabelsJSON())
		m.LabelsJSON = &v
	}
	if rq.DefaultHeadersJSON != nil {
		v := strings.TrimSpace(rq.GetDefaultHeadersJSON())
		m.DefaultHeadersJSON = &v
	}
	if rq.TlsConfigJSON != nil {
		v := strings.TrimSpace(rq.GetTlsConfigJSON())
		m.TLSConfigJSON = &v
	}

	applyDatasourceDefaults(m)

	if err := b.store.Datasource().Update(ctx, m); err != nil {
		return nil, errno.ErrDatasourceUpdateFailed
	}

	return &v1.UpdateDatasourceResponse{}, nil
}

func (b *datasourceBiz) Delete(ctx context.Context, rq *v1.DeleteDatasourceRequest) (*v1.DeleteDatasourceResponse, error) {
	// P0 uses soft-delete semantics by disabling datasource.
	m, err := b.store.Datasource().Get(ctx, where.T(ctx).F("datasource_id", rq.GetDatasourceID()))
	if err != nil {
		return nil, toDatasourceGetError(err)
	}
	m.IsEnabled = false
	if err := b.store.Datasource().Update(ctx, m); err != nil {
		return nil, errno.ErrDatasourceDeleteFailed
	}
	return &v1.DeleteDatasourceResponse{}, nil
}

func (b *datasourceBiz) Get(ctx context.Context, rq *v1.GetDatasourceRequest) (*v1.GetDatasourceResponse, error) {
	m, err := b.store.Datasource().Get(ctx, where.T(ctx).F("datasource_id", rq.GetDatasourceID()))
	if err != nil {
		return nil, toDatasourceGetError(err)
	}
	return &v1.GetDatasourceResponse{Datasource: conversion.DatasourceMToDatasourceV1(m)}, nil
}

func (b *datasourceBiz) List(ctx context.Context, rq *v1.ListDatasourceRequest) (*v1.ListDatasourceResponse, error) {
	whr := where.T(ctx).P(int(rq.GetOffset()), int(rq.GetLimit()))
	if rq.Type != nil && strings.TrimSpace(rq.GetType()) != "" {
		whr = whr.F("type", strings.ToLower(strings.TrimSpace(rq.GetType())))
	}
	if rq.IsEnabled != nil {
		whr = whr.F("is_enabled", rq.GetIsEnabled())
	}
	if rq.Name != nil && strings.TrimSpace(rq.GetName()) != "" {
		keyword := "%" + strings.TrimSpace(rq.GetName()) + "%"
		whr = whr.Q("name LIKE ?", keyword)
	}

	total, list, err := b.store.Datasource().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrDatasourceListFailed
	}

	out := make([]*v1.Datasource, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.DatasourceMToDatasourceV1(item))
	}

	return &v1.ListDatasourceResponse{
		TotalCount:  total,
		Datasources: out,
	}, nil
}

func applyDatasourceDefaults(m *model.DatasourceM) {
	m.Type = strings.ToLower(strings.TrimSpace(m.Type))
	m.Name = strings.TrimSpace(m.Name)
	m.BaseURL = strings.TrimSpace(m.BaseURL)
	m.AuthType = strings.ToLower(strings.TrimSpace(m.AuthType))

	if m.AuthType == "" {
		m.AuthType = "none"
	}
	if m.TimeoutMs <= 0 {
		m.TimeoutMs = 5000
	}
}

func toDatasourceGetError(err error) error {
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrDatasourceNotFound
	}
	return errno.ErrDatasourceGetFailed
}
