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

func TestParseUseLatestChannel(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantValue bool
		wantErr   bool
	}{
		{
			name:    "default empty",
			raw:     "",
			wantErr: false,
		},
		{
			name:      "zero",
			raw:       "0",
			wantValue: false,
		},
		{
			name:      "one",
			raw:       "1",
			wantValue: true,
		},
		{
			name:      "trim spaces",
			raw:       " 1 ",
			wantValue: true,
		},
		{
			name:    "invalid true",
			raw:     "true",
			wantErr: true,
		},
		{
			name:    "invalid number",
			raw:     "2",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUseLatestChannel(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantValue, got)
		})
	}
}
