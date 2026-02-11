package auth

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

const (
	envJWTSecret          = "RCA_API_AUTH_JWT_SECRET"
	defaultJWTSecret      = "rca-api-dev-jwt-secret"
	defaultTokenTTL       = 12 * time.Hour
	defaultIssuer         = "rca-api"
	defaultAudience       = "rca-api-operator"
	maxTokenTTL           = 7 * 24 * time.Hour
	defaultOperatorPrefix = "operator:"
)

// Claims defines operator identity claims used by session/operator APIs.
type Claims struct {
	OperatorID string   `json:"operator_id"`
	Username   string   `json:"username,omitempty"`
	TeamIDs    []string `json:"team_ids,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
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
}

// IssueTokenResponse includes signed token and parsed claims.
type IssueTokenResponse struct {
	Token     string
	ExpiresAt time.Time
	Claims    *Claims
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
	ttl := rq.TTL
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	if ttl > maxTokenTTL {
		return nil, errno.ErrInvalidArgument
	}
	expiresAt := now.Add(ttl)

	claims := &Claims{
		OperatorID: operatorID,
		Username:   strings.TrimSpace(rq.Username),
		TeamIDs:    normalizeStringList(rq.TeamIDs),
		Scopes:     normalizeStringList(rq.Scopes),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    defaultIssuer,
			Subject:   operatorID,
			Audience:  jwt.ClaimStrings{defaultAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
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
	}, nil
}

// ParseToken validates and parses one Bearer token.
func ParseToken(token string) (*Claims, error) {
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
		jwt.WithAudience(defaultAudience),
		jwt.WithIssuer(defaultIssuer),
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
	if claims.ExpiresAt == nil || claims.ExpiresAt.Time.IsZero() {
		return nil, errno.ErrTokenInvalid
	}
	if time.Now().UTC().After(claims.ExpiresAt.Time.UTC()) {
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

func IsTokenError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errno.ErrTokenInvalid) || errors.Is(err, errno.ErrUnauthenticated)
}
