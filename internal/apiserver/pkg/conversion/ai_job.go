package conversion

import (
	"github.com/onexstack/onexstack/pkg/core"

	"zk8s.com/rca-api/internal/apiserver/model"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

// AIJobMToAIJobV1 converts AIJob model to v1 message.
func AIJobMToAIJobV1(m *model.AIJobM) *v1.AIJob {
	var out v1.AIJob
	_ = core.CopyWithConverters(&out, m)
	return &out
}

// AIToolCallMToAIToolCallV1 converts AIToolCall model to v1 message.
func AIToolCallMToAIToolCallV1(m *model.AIToolCallM) *v1.AIToolCall {
	var out v1.AIToolCall
	_ = core.CopyWithConverters(&out, m)
	return &out
}
