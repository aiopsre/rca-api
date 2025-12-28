package model

// NoticeDeliverySnapshot stores immutable channel config copied into one delivery.
type NoticeDeliverySnapshot struct {
	EndpointURL       *string           `json:"endpoint_url,omitempty"`
	TimeoutMs         *int64            `json:"timeout_ms,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	SecretFingerprint *string           `json:"secret_fingerprint,omitempty"`
	ChannelVersion    *int64            `json:"channel_version,omitempty"`
}
