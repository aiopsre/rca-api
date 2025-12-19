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
	incident.RcaStatus = strPtr(incidentM.RCAStatus)
	incident.RootCauseSummary = cloneStrPtr(incidentM.RootCauseSummary)
	incident.DiagnosisJSON = cloneStrPtr(incidentM.DiagnosisJSON)
	incident.EvidenceRefsJSON = cloneStrPtr(incidentM.EvidenceRefsJSON)
	return &incident
}

// IncidentV1ToIncidentM converts a Incident object from the v1 API format
// to a IncidentM object in the internal model.
func IncidentV1ToIncidentM(incident *v1.Incident) *model.IncidentM {
	var incidentM model.IncidentM
	_ = core.CopyWithConverters(&incidentM, incident)
	if incident == nil {
		return &incidentM
	}
	if incident.RcaStatus != nil {
		incidentM.RCAStatus = incident.GetRcaStatus()
	}
	if incident.RootCauseSummary != nil {
		incidentM.RootCauseSummary = cloneStrPtr(incident.RootCauseSummary)
	}
	if incident.DiagnosisJSON != nil {
		incidentM.DiagnosisJSON = cloneStrPtr(incident.DiagnosisJSON)
	}
	if incident.EvidenceRefsJSON != nil {
		incidentM.EvidenceRefsJSON = cloneStrPtr(incident.EvidenceRefsJSON)
	}
	return &incidentM
}

func cloneStrPtr(v *string) *string {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func strPtr(v string) *string {
	c := v
	return &c
}
