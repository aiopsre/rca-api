package conversion

import (
	"github.com/onexstack/onexstack/pkg/core"

	"zk8s.com/rca-api/internal/apiserver/model"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

// EvidenceMToEvidenceV1 converts Evidence model to v1 message.
func EvidenceMToEvidenceV1(m *model.EvidenceM) *v1.Evidence {
	var out v1.Evidence
	_ = core.CopyWithConverters(&out, m)
	return &out
}
