package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

const (
	envJWTSecret          = "RCA_API_AUTH_JWT_SECRET"
	envJWTIssuer          = "RCA_API_AUTH_JWT_ISSUER"
	envJWTAudience        = "RCA_API_AUTH_JWT_AUDIENCE"
	envAccessTokenTTL     = "RCA_API_AUTH_ACCESS_TOKEN_TTL"
	envRefreshTokenTTL    = "RCA_API_AUTH_REFRESH_TOKEN_TTL"
	envTokenClockSkew     = "RCA_API_AUTH_TOKEN_CLOCK_SKEW"
	defaultJWTSecret      = "rca-api-dev-jwt-secret"
	defaultIssuer         = "rca-api"
	defaultAudience       = "rca-api-operator"
	defaultAccessTokenTTL = 30 * time.Minute
	defaultRefreshTokenTL = 7 * 24 * time.Hour
	maxTokenTTL           = 30 * 24 * time.Hour
	defaultTokenClockSkew = 5 * time.Second
	defaultOperatorPrefix = "operator:"
	tokenTypeAccess       = "access"
	tokenTypeRefresh      = "refresh"
)

// Claims defines operator identity claims used by session/operator APIs.
type Claims struct {
	OperatorID string   `json:"operator_id"`
	Username   string   `json:"username,omitempty"`
	TeamIDs    []string `json:"team_ids,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
	TokenType  string   `json:"token_type,omitempty"`
	jwt.RegisteredClaims
}

// IssueTokenRequest provides inputs for token issuance.
type IssueTokenRequest struct {
	OperatorID string
	Username   string
	TeamIDs    []string
	Scopes     []string
	Now        time.Time
	TTL        time.Duration
	TokenType  string
}

// IssueTokenResponse includes signed token and parsed claims.
type IssueTokenResponse struct {
	Token     string
	ExpiresAt time.Time
	Claims    *Claims
	TokenType string
}

// IssueTokenPairRequest contains inputs for access+refresh token issuance.
type IssueTokenPairRequest struct {
	OperatorID string
	Username   string
	TeamIDs    []string
	Scopes     []string
	Now        time.Time
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// IssueTokenPairResponse includes one access token and one refresh token.
type IssueTokenPairResponse struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	Claims           *Claims
}

// IssueToken signs one operator token for API calls.
func IssueToken(rq *IssueTokenRequest) (*IssueTokenResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	operatorID := normalizeOperatorID(rq.OperatorID)
	if operatorID == "" {
		return nil, errno.ErrInvalidArgument
	}
	now := rq.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tokenType := normalizeTokenType(rq.TokenType)
	ttl := resolveTokenTTL(tokenType, rq.TTL)
	if ttl > maxTokenTTL {
		return nil, errno.ErrInvalidArgument
	}
	expiresAt := now.Add(ttl)

	claims := &Claims{
		OperatorID: operatorID,
		Username:   strings.TrimSpace(rq.Username),
		TeamIDs:    normalizeStringList(rq.TeamIDs),
		Scopes:     normalizeStringList(rq.Scopes),
		TokenType:  tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer(),
			Subject:   operatorID,
			Audience:  jwt.ClaimStrings{jwtAudience()},
			ID:        newTokenID(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now.Add(-tokenClockSkew())),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret())
	if err != nil {
		return nil, errno.ErrSignToken
	}
	return &IssueTokenResponse{
		Token:     signed,
		ExpiresAt: expiresAt,
		Claims:    claims,
		TokenType: tokenType,
	}, nil
}

// IssueTokenPair signs one access token and one refresh token.
func IssueTokenPair(rq *IssueTokenPairRequest) (*IssueTokenPairResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	now := rq.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	accessResp, err := IssueToken(&IssueTokenRequest{
		OperatorID: rq.OperatorID,
		Username:   rq.Username,
		TeamIDs:    rq.TeamIDs,
		Scopes:     rq.Scopes,
		Now:        now,
		TTL:        rq.AccessTTL,
		TokenType:  tokenTypeAccess,
	})
	if err != nil {
		return nil, err
	}
	refreshResp, err := IssueToken(&IssueTokenRequest{
		OperatorID: rq.OperatorID,
		Username:   rq.Username,
		TeamIDs:    rq.TeamIDs,
		Scopes:     rq.Scopes,
		Now:        now,
		TTL:        rq.RefreshTTL,
		TokenType:  tokenTypeRefresh,
	})
	if err != nil {
		return nil, err
	}
	return &IssueTokenPairResponse{
		AccessToken:      accessResp.Token,
		AccessExpiresAt:  accessResp.ExpiresAt,
		RefreshToken:     refreshResp.Token,
		RefreshExpiresAt: refreshResp.ExpiresAt,
		Claims:           accessResp.Claims,
	}, nil
}

// RotateTokenPair validates one refresh token and issues new access+refresh tokens.
func RotateTokenPair(refreshToken string, now time.Time, accessTTL time.Duration, refreshTTL time.Duration) (*IssueTokenPairResponse, error) {
	claims, err := ParseRefreshToken(refreshToken)
	if err != nil {
		return nil, err
	}
	return IssueTokenPair(&IssueTokenPairRequest{
		OperatorID: claims.OperatorID,
		Username:   claims.Username,
		TeamIDs:    claims.TeamIDs,
		Scopes:     claims.Scopes,
		Now:        now,
		AccessTTL:  accessTTL,
		RefreshTTL: refreshTTL,
	})
}

// ParseToken validates and parses one Bearer access token.
// It keeps compatibility with existing callers.
func ParseToken(token string) (*Claims, error) {
	return ParseAccessToken(token)
}

// ParseAccessToken validates and parses one Bearer access token.
func ParseAccessToken(token string) (*Claims, error) {
	return parseTokenWithExpectedType(token, tokenTypeAccess)
}

// ParseRefreshToken validates and parses one refresh token.
func ParseRefreshToken(token string) (*Claims, error) {
	return parseTokenWithExpectedType(token, tokenTypeRefresh)
}

func parseTokenWithExpectedType(token string, expectedType string) (*Claims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errno.ErrTokenInvalid
	}
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if t == nil {
			return nil, errno.ErrTokenInvalid
		}
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errno.ErrTokenInvalid
		}
		return jwtSecret(), nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithAudience(jwtAudience()),
		jwt.WithIssuer(jwtIssuer()),
	)
	if err != nil {
		return nil, errno.ErrTokenInvalid
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || claims == nil {
		return nil, errno.ErrTokenInvalid
	}
	if !parsed.Valid {
		return nil, errno.ErrTokenInvalid
	}
	claims.OperatorID = normalizeOperatorID(claims.OperatorID)
	if claims.OperatorID == "" {
		claims.OperatorID = normalizeOperatorID(claims.Subject)
	}
	if claims.OperatorID == "" {
		return nil, errno.ErrTokenInvalid
	}
	claims.Username = strings.TrimSpace(claims.Username)
	claims.TeamIDs = normalizeStringList(claims.TeamIDs)
	claims.Scopes = normalizeStringList(claims.Scopes)
	claims.TokenType = normalizeTokenType(claims.TokenType)
	if claims.ExpiresAt == nil || claims.ExpiresAt.Time.IsZero() {
		return nil, errno.ErrTokenInvalid
	}
	if time.Now().UTC().After(claims.ExpiresAt.Time.UTC()) {
		return nil, errno.ErrTokenInvalid
	}
	claimType := claims.TokenType
	if claimType == "" {
		claimType = tokenTypeAccess
	}
	if claimType != normalizeTokenType(expectedType) {
		return nil, errno.ErrTokenInvalid
	}
	return claims, nil
}

func ExtractBearerToken(headerValue string) (string, error) {
	headerValue = strings.TrimSpace(headerValue)
	if headerValue == "" {
		return "", errno.ErrUnauthenticated
	}
	parts := strings.Fields(headerValue)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errno.ErrUnauthenticated
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", errno.ErrUnauthenticated
	}
	return token, nil
}

func normalizeOperatorID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, defaultOperatorPrefix) {
		return trimmed
	}
	return trimmed
}

func normalizeStringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func jwtSecret() []byte {
	secret := strings.TrimSpace(os.Getenv(envJWTSecret))
	if secret == "" {
		secret = defaultJWTSecret
	}
	return []byte(secret)
}

func jwtIssuer() string {
	issuer := strings.TrimSpace(os.Getenv(envJWTIssuer))
	if issuer == "" {
		return defaultIssuer
	}
	return issuer
}

func jwtAudience() string {
	audience := strings.TrimSpace(os.Getenv(envJWTAudience))
	if audience == "" {
		return defaultAudience
	}
	return audience
}

func resolveTokenTTL(tokenType string, ttl time.Duration) time.Duration {
	if ttl > 0 {
		return ttl
	}
	if normalizeTokenType(tokenType) == tokenTypeRefresh {
		return refreshTokenTTL()
	}
	return accessTokenTTL()
}

func accessTokenTTL() time.Duration {
	return parseDurationEnv(envAccessTokenTTL, defaultAccessTokenTTL)
}

func refreshTokenTTL() time.Duration {
	return parseDurationEnv(envRefreshTokenTTL, defaultRefreshTokenTL)
}

func tokenClockSkew() time.Duration {
	return parseDurationEnv(envTokenClockSkew, defaultTokenClockSkew)
}

func parseDurationEnv(envName string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return fallback
	}
	if asInt, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if asInt <= 0 {
			return fallback
		}
		return time.Duration(asInt) * time.Second
	}
	if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
		return parsed
	}
	return fallback
}

func normalizeTokenType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case tokenTypeRefresh:
		return tokenTypeRefresh
	default:
		return tokenTypeAccess
	}
}

func newTokenID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
}

func IsTokenError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errno.ErrTokenInvalid) || errors.Is(err, errno.ErrUnauthenticated)
}
