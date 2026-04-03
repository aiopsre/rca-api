package auth

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
	require.Equal(t, "access", issueResp.TokenType)

	claims, err := ParseToken(issueResp.Token)
	require.NoError(t, err)
	require.Equal(t, "operator:alice", claims.OperatorID)
	require.Equal(t, "alice", claims.Username)
	require.Equal(t, []string{"payments"}, claims.TeamIDs)
	require.Equal(t, []string{"ai.read", "ai.run"}, claims.Scopes)
	require.Equal(t, "access", claims.TokenType)
	require.True(t, claims.ExpiresAt.Time.After(now))
}

func TestIssueTokenPairAndRefreshParse(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pair, err := IssueTokenPair(&IssueTokenPairRequest{
		OperatorID: "operator:bob",
		Username:   "bob",
		TeamIDs:    []string{"namespace:payments"},
		Scopes:     []string{"ai.read"},
		Now:        now,
		AccessTTL:  10 * time.Minute,
		RefreshTTL: 2 * time.Hour,
	})
	require.NoError(t, err)
	require.NotEmpty(t, pair.AccessToken)
	require.NotEmpty(t, pair.RefreshToken)
	require.True(t, pair.RefreshExpiresAt.After(pair.AccessExpiresAt))

	accessClaims, err := ParseAccessToken(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "access", accessClaims.TokenType)

	refreshClaims, err := ParseRefreshToken(pair.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, "refresh", refreshClaims.TokenType)

	_, err = ParseAccessToken(pair.RefreshToken)
	require.Error(t, err)

	rotated, err := RotateTokenPair(pair.RefreshToken, now.Add(1*time.Minute), 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, rotated.AccessToken)
	require.NotEmpty(t, rotated.RefreshToken)
	require.NotEqual(t, pair.AccessToken, rotated.AccessToken)
	require.NotEqual(t, pair.RefreshToken, rotated.RefreshToken)
}

func TestTokenTTLFromEnv(t *testing.T) {
	t.Setenv(envAccessTokenTTL, "45s")
	t.Setenv(envRefreshTokenTTL, "3600")
	resp, err := IssueTokenPair(&IssueTokenPairRequest{OperatorID: "alice"})
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().UTC().Add(45*time.Second), resp.AccessExpiresAt, 3*time.Second)
	require.WithinDuration(t, time.Now().UTC().Add(1*time.Hour), resp.RefreshExpiresAt, 5*time.Second)
}

func TestVerifyOIDCIDToken_HS256(t *testing.T) {
	t.Setenv(envOIDCEnabled, "true")
	t.Setenv(envOIDCIssuer, "https://issuer.example")
	t.Setenv(envOIDCAudience, "rca-operator-ui")
	t.Setenv(envOIDCHMACSecret, "oidc-secret")
	t.Setenv(envOIDCRSAPublicPEM, "")

	now := time.Now().UTC()
	oidcClaims := jwt.MapClaims{
		"iss":                "https://issuer.example",
		"aud":                "rca-operator-ui",
		"sub":                "oidc-user-1",
		"preferred_username": "alice",
		"team_ids":           []any{"namespace:payments", "tenant:tenant-a"},
		"scope":              "ai.read session.review",
		"exp":                now.Add(30 * time.Minute).Unix(),
		"nbf":                now.Add(-1 * time.Minute).Unix(),
		"iat":                now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, oidcClaims)
	signed, err := token.SignedString([]byte("oidc-secret"))
	require.NoError(t, err)

	identity, err := VerifyOIDCIDToken(signed)
	require.NoError(t, err)
	require.Equal(t, "oidc-user-1", identity.Subject)
	require.Equal(t, "alice", identity.Username)
	require.Contains(t, identity.TeamIDs, "namespace:payments")
	require.Contains(t, identity.Scopes, "ai.read")
	require.Contains(t, identity.Scopes, "session.review")
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

func TestParseDurationEnv_Fallback(t *testing.T) {
	t.Setenv(envAccessTokenTTL, "")
	t.Setenv(envRefreshTokenTTL, "")
	require.Equal(t, defaultAccessTokenTTL, accessTokenTTL())
	require.Equal(t, defaultRefreshTokenTL, refreshTokenTTL())

	_ = os.Unsetenv(envAccessTokenTTL)
	_ = os.Unsetenv(envRefreshTokenTTL)
}
