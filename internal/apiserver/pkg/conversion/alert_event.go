package conversion

import (
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

// AlertEventMToAlertEventV1 converts model alert event to API object.
func AlertEventMToAlertEventV1(alertEventM *model.AlertEventM) *v1.AlertEvent {
	var out v1.AlertEvent
	_ = core.CopyWithConverters(&out, alertEventM)
	return &out
}
