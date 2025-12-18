package rid

import (
	"github.com/onexstack/onexstack/pkg/id"
)

const defaultABC = "abcdefghijklmnopqrstuvwxyz1234567890"

type ResourceID string

const (
	// IncidentID 定义用户资源标识符.
	IncidentID ResourceID = "incident"
	// AlertEventID defines alert event resource identifier.
	AlertEventID ResourceID = "alert-event"
	// DatasourceID defines datasource resource identifier.
	DatasourceID ResourceID = "datasource"
	// EvidenceID defines evidence resource identifier.
	EvidenceID ResourceID = "evidence"
	// AIJobID defines ai job resource identifier.
	AIJobID ResourceID = "ai-job"
	// AIToolCallID defines ai tool call resource identifier.
	AIToolCallID ResourceID = "tool-call"
)

// String 将资源标识符转换为字符串.
func (rid ResourceID) String() string {
	return string(rid)
}

// New 创建带前缀的唯一标识符.
func (rid ResourceID) New(counter uint64) string {
	// 使用自定义选项生成唯一标识符
	uniqueStr := id.NewCode(
		counter,
		id.WithCodeChars([]rune(defaultABC)),
		id.WithCodeL(6),
		id.WithCodeSalt(Salt()),
	)
	return rid.String() + "-" + uniqueStr
}
