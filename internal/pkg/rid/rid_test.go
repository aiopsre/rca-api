package rid_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// Mock Salt function used for testing
func Salt() string {
	return "staticSalt"
}

func TestResourceID_String(t *testing.T) {
	// 测试 IncidentID 转换为字符串
	incidentID := rid.IncidentID
	assert.Equal(t, "incident", incidentID.String(), "incidentID.String() should return 'incident'")

	// 测试 DatasourceID 转换为字符串
	datasourceID := rid.DatasourceID
	assert.Equal(t, "datasource", datasourceID.String(), "datasourceID.String() should return 'datasource'")

	// 测试 EvidenceID 转换为字符串
	evidenceID := rid.EvidenceID
	assert.Equal(t, "evidence", evidenceID.String(), "evidenceID.String() should return 'evidence'")

	// 测试 AIJobID 转换为字符串
	aiJobID := rid.AIJobID
	assert.Equal(t, "ai-job", aiJobID.String(), "aiJobID.String() should return 'ai-job'")

	// 测试 AIToolCallID 转换为字符串
	toolCallID := rid.AIToolCallID
	assert.Equal(t, "tool-call", toolCallID.String(), "toolCallID.String() should return 'tool-call'")

	// 测试 SilenceID 转换为字符串
	silenceID := rid.SilenceID
	assert.Equal(t, "silence", silenceID.String(), "silenceID.String() should return 'silence'")
}

func TestResourceID_New(t *testing.T) {
	// 测试生成的ID是否带有正确前缀
	incidentID := rid.IncidentID
	uniqueID := incidentID.New(1)

	assert.True(t, len(uniqueID) > 0, "Generated ID should not be empty")
	assert.Contains(t, uniqueID, "incident-", "Generated ID should start with 'incident-' prefix")

	// 生成另外一个唯一标识符，确保生成的值不同（唯一性）
	anotherID := incidentID.New(2)
	assert.NotEqual(t, uniqueID, anotherID, "Generated IDs should be unique")
}

func BenchmarkResourceID_New(b *testing.B) {
	// 性能测试
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		incidentID := rid.IncidentID
		_ = incidentID.New(uint64(i))
	}
}

func FuzzResourceID_New(f *testing.F) {
	// 添加预置测试数据
	f.Add(uint64(1))      // 添加一个种子值counter为1
	f.Add(uint64(123456)) // 添加一个较大的种子值

	f.Fuzz(func(t *testing.T, counter uint64) {
		// 测试IncidentID的New方法
		result := rid.IncidentID.New(counter)

		// 断言结果不为空
		assert.NotEmpty(t, result, "The generated unique identifier should not be empty")

		// 断言结果必须包含资源标识符前缀
		assert.Contains(t, result, rid.IncidentID.String()+"-", "The generated unique identifier should contain the correct prefix")

		// 断言前缀不会与uniqueStr部分重叠
		splitParts := strings.SplitN(result, "-", 2)
		assert.Equal(t, rid.IncidentID.String(), splitParts[0], "The prefix part of the result should correctly match the IncidentID")

		// 断言生成的ID具有固定长度（基于NewCode的配置）
		if len(splitParts) == 2 {
			assert.Equal(t, 6, len(splitParts[1]), "The unique identifier part should have a length of 6")
		} else {
			t.Errorf("The format of the generated unique identifier does not meet expectation")
		}
	})
}
