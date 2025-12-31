package conversion

import (
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

// DatasourceMToDatasourceV1 converts Datasource model to v1 message.
func DatasourceMToDatasourceV1(m *model.DatasourceM) *v1.Datasource {
	var out v1.Datasource
	_ = core.CopyWithConverters(&out, m)
	return &out
}
