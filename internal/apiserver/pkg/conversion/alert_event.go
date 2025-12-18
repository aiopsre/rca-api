package conversion

import (
	"github.com/onexstack/onexstack/pkg/core"

	"zk8s.com/rca-api/internal/apiserver/model"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

// AlertEventMToAlertEventV1 converts model alert event to API object.
func AlertEventMToAlertEventV1(alertEventM *model.AlertEventM) *v1.AlertEvent {
	var out v1.AlertEvent
	_ = core.CopyWithConverters(&out, alertEventM)
	return &out
}
