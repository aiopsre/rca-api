package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseNoticeDeliveryAction(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantID     string
		wantOp     string
		wantParsed bool
	}{
		{
			name:       "colon replay",
			raw:        "notice-delivery-1:replay",
			wantID:     "notice-delivery-1",
			wantOp:     "replay",
			wantParsed: true,
		},
		{
			name:       "colon cancel with slash prefix",
			raw:        "/notice-delivery-2:cancel",
			wantID:     "notice-delivery-2",
			wantOp:     "cancel",
			wantParsed: true,
		},
		{
			name:       "slash replay",
			raw:        "notice-delivery-3/replay",
			wantID:     "notice-delivery-3",
			wantOp:     "replay",
			wantParsed: true,
		},
		{
			name:       "invalid empty",
			raw:        "",
			wantParsed: false,
		},
		{
			name:       "invalid action",
			raw:        "notice-delivery-4:noop",
			wantParsed: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotOp, gotParsed := parseNoticeDeliveryAction(tt.raw)
			require.Equal(t, tt.wantParsed, gotParsed)
			if tt.wantParsed {
				require.Equal(t, tt.wantID, gotID)
				require.Equal(t, tt.wantOp, gotOp)
			}
		})
	}
}
