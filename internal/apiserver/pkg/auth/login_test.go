package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIssueAndParseToken(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	issueResp, err := IssueToken(&IssueTokenRequest{
		OperatorID: "operator:alice",
		Username:   "alice",
		TeamIDs:    []string{"payments", "payments", "  "},
		Scopes:     []string{"ai.read", "ai.run"},
		Now:        now,
		TTL:        2 * time.Hour,
	})
	require.NoError(t, err)
	require.NotEmpty(t, issueResp.Token)
	require.Equal(t, "operator:alice", issueResp.Claims.OperatorID)
	require.Equal(t, []string{"payments"}, issueResp.Claims.TeamIDs)

	claims, err := ParseToken(issueResp.Token)
	require.NoError(t, err)
	require.Equal(t, "operator:alice", claims.OperatorID)
	require.Equal(t, "alice", claims.Username)
	require.Equal(t, []string{"payments"}, claims.TeamIDs)
	require.Equal(t, []string{"ai.read", "ai.run"}, claims.Scopes)
	require.True(t, claims.ExpiresAt.Time.After(now))
}

func TestExtractBearerToken(t *testing.T) {
	token, err := ExtractBearerToken("Bearer abc")
	require.NoError(t, err)
	require.Equal(t, "abc", token)

	_, err = ExtractBearerToken("abc")
	require.Error(t, err)

	_, err = ExtractBearerToken("Bearer ")
	require.Error(t, err)
}

func TestParseToken_Invalid(t *testing.T) {
	_, err := ParseToken("not-a-token")
	require.Error(t, err)
}
