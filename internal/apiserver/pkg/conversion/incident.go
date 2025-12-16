package conversion

import (
	"github.com/onexstack/onexstack/pkg/core"

	"zk8s.com/rca-api/internal/apiserver/model"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

// IncidentMToIncidentV1 converts a IncidentM object from the internal model
// to a Incident object in the v1 API format.
func IncidentMToIncidentV1(incidentM *model.IncidentM) *v1.Incident {
	var incident v1.Incident
	_ = core.CopyWithConverters(&incident, incidentM)
	return &incident
}

// IncidentV1ToIncidentM converts a Incident object from the v1 API format
// to a IncidentM object in the internal model.
func IncidentV1ToIncidentM(incident *v1.Incident) *model.IncidentM {
	var incidentM model.IncidentM
	_ = core.CopyWithConverters(&incidentM, incident)
	return &incidentM
}
