package conversion

import (
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

// IncidentActionLogMToOperatorActionLogV1 converts model action-log to API object.
func IncidentActionLogMToOperatorActionLogV1(m *model.IncidentActionLogM) *v1.OperatorActionLog {
	var out v1.OperatorActionLog
	_ = core.CopyWithConverters(&out, m)
	if m == nil {
		return &out
	}
	out.DetailsJSON = cloneStrPtr(m.DetailsJSON)
	return &out
}
