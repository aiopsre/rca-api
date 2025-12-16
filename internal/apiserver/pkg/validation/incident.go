package validation

import (
	"context"

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

// ValidateIncidentRules returns a set of validation rules for incident-related requests.
func (v *Validator) ValidateIncidentRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// ValidateCreateIncidentRequest validates the fields of a CreateIncidentRequest.
func (v *Validator) ValidateCreateIncidentRequest(ctx context.Context, rq *v1.CreateIncidentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
}

// ValidateUpdateIncidentRequest validates the fields of an UpdateIncidentRequest.
func (v *Validator) ValidateUpdateIncidentRequest(ctx context.Context, rq *v1.UpdateIncidentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
}

// ValidateDeleteIncidentRequest validates the fields of a DeleteIncidentRequest.
func (v *Validator) ValidateDeleteIncidentRequest(ctx context.Context, rq *v1.DeleteIncidentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
}

//// ValidateDeleteIncidentsRequest validates the fields of a DeleteIncidentsRequest.
//func (v *Validator) ValidateDeleteIncidentsRequest(ctx context.Context, rq *v1.DeleteIncidentsRequest) error {
//	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
//}

// ValidateGetIncidentRequest validates the fields of a GetIncidentRequest.
func (v *Validator) ValidateGetIncidentRequest(ctx context.Context, rq *v1.GetIncidentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
}

// ValidateListIncidentRequest validates the fields of a ListIncidentRequest, focusing on selected fields ("Offset" and "Limit").
func (v *Validator) ValidateListIncidentRequest(ctx context.Context, rq *v1.ListIncidentRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, v.ValidateIncidentRules(), "Offset", "Limit")
}
