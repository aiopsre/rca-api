package notice

import (
	"os"
	"strings"
	"sync/atomic"
)

var noticeBaseURLConfigured atomic.Value

// SetConfiguredNoticeBaseURL sets process-level default links base_url from server config.
// Empty values clear the config override and fallback to NOTICE_BASE_URL.
func SetConfiguredNoticeBaseURL(baseURL string) {
	noticeBaseURLConfigured.Store(strings.TrimSpace(baseURL))
}

func configuredNoticeBaseURL() string {
	if configured, ok := noticeBaseURLConfigured.Load().(string); ok {
		configured = strings.TrimSpace(configured)
		if configured != "" {
			return configured
		}
	}
	return strings.TrimSpace(os.Getenv(NoticeBaseURLEnvName))
}
