package handler

import (
	"testing"

	"github.com/stretchr/testify/require"

	triggerbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/trigger"
)

func TestResolveManualTrigger_DefaultManual(t *testing.T) {
	triggerType, triggerSource := resolveManualTrigger(nil)
	require.Equal(t, triggerbiz.TriggerTypeManual, triggerType)
	require.Equal(t, "manual_api", triggerSource)
}

func TestResolveManualTrigger_Replay(t *testing.T) {
	raw := "replay"
	triggerType, triggerSource := resolveManualTrigger(&raw)
	require.Equal(t, triggerbiz.TriggerTypeReplay, triggerType)
	require.Equal(t, "manual_replay_api", triggerSource)
}

func TestResolveManualTrigger_FollowUp(t *testing.T) {
	raw := "follow_up"
	triggerType, triggerSource := resolveManualTrigger(&raw)
	require.Equal(t, triggerbiz.TriggerTypeFollowUp, triggerType)
	require.Equal(t, "manual_follow_up_api", triggerSource)
}
