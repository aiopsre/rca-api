package model

// NoticeSelectors holds channel-level allow-list rules used by notice routing.
type NoticeSelectors struct {
	EventTypes     []string `json:"event_types,omitempty"`
	Namespaces     []string `json:"namespaces,omitempty"`
	Services       []string `json:"services,omitempty"`
	Severities     []string `json:"severities,omitempty"`
	RootCauseTypes []string `json:"root_cause_types,omitempty"`
}
