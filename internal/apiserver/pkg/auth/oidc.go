package auth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

const (
	envOIDCEnabled      = "RCA_API_AUTH_OIDC_ENABLED"
	envOIDCIssuer       = "RCA_API_AUTH_OIDC_ISSUER"
	envOIDCAudience     = "RCA_API_AUTH_OIDC_AUDIENCE"
	envOIDCHMACSecret   = "RCA_API_AUTH_OIDC_HS256_SECRET"
	envOIDCRSAPublicPEM = "RCA_API_AUTH_OIDC_RS256_PUBLIC_KEY_PEM"
)

// OIDCIdentity is the normalized identity resolved from one upstream OIDC ID token.
type OIDCIdentity struct {
	Subject  string
	Username string
	Email    string
	TeamIDs  []string
	Scopes   []string
}

// VerifyOIDCIDToken verifies one OIDC ID token using static issuer/audience/key config.
func VerifyOIDCIDToken(idToken string) (*OIDCIdentity, error) {
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return nil, errno.ErrUnauthenticated
	}
	if !oidcEnabled() {
		return nil, errno.ErrUnauthenticated
	}

	issuer := strings.TrimSpace(os.Getenv(envOIDCIssuer))
	audience := strings.TrimSpace(os.Getenv(envOIDCAudience))
	hmacSecret := strings.TrimSpace(os.Getenv(envOIDCHMACSecret))
	rsaPublicPEM := strings.TrimSpace(os.Getenv(envOIDCRSAPublicPEM))

	var rsaPublicKey *rsa.PublicKey
	if rsaPublicPEM != "" {
		parsedKey, err := parseRSAPublicKeyFromPEM(rsaPublicPEM)
		if err != nil {
			return nil, errno.ErrTokenInvalid
		}
		rsaPublicKey = parsedKey
	}

	parserOptions := []jwt.ParserOption{
		jwt.WithLeeway(tokenClockSkew()),
		jwt.WithValidMethods([]string{
			jwt.SigningMethodHS256.Alg(),
			jwt.SigningMethodHS384.Alg(),
			jwt.SigningMethodHS512.Alg(),
			jwt.SigningMethodRS256.Alg(),
			jwt.SigningMethodRS384.Alg(),
			jwt.SigningMethodRS512.Alg(),
		}),
	}
	if issuer != "" {
		parserOptions = append(parserOptions, jwt.WithIssuer(issuer))
	}
	if audience != "" {
		parserOptions = append(parserOptions, jwt.WithAudience(audience))
	}

	parsedToken, err := jwt.Parse(idToken, func(t *jwt.Token) (any, error) {
		if t == nil {
			return nil, errno.ErrTokenInvalid
		}
		switch t.Method.(type) {
		case *jwt.SigningMethodHMAC:
			if hmacSecret == "" {
				return nil, errno.ErrTokenInvalid
			}
			return []byte(hmacSecret), nil
		case *jwt.SigningMethodRSA:
			if rsaPublicKey == nil {
				return nil, errno.ErrTokenInvalid
			}
			return rsaPublicKey, nil
		default:
			return nil, errno.ErrTokenInvalid
		}
	}, parserOptions...)
	if err != nil {
		return nil, errno.ErrTokenInvalid
	}
	if parsedToken == nil || !parsedToken.Valid {
		return nil, errno.ErrTokenInvalid
	}

	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errno.ErrTokenInvalid
	}

	subject := firstNonEmptyClaim(claims, "sub", "uid", "user_id", "preferred_username", "email")
	if subject == "" {
		return nil, errno.ErrTokenInvalid
	}
	username := firstNonEmptyClaim(claims, "preferred_username", "name", "username")
	email := firstNonEmptyClaim(claims, "email")
	teamIDs := collectStringClaimValues(claims, "team_ids", "teams", "groups")
	tenantID := firstNonEmptyClaim(claims, "tenant", "tenant_id")
	if tenantID != "" {
		teamIDs = append(teamIDs, "tenant:"+strings.ToLower(strings.TrimSpace(tenantID)))
	}
	scopeItems := collectStringClaimValues(claims, "scope", "scp", "scopes", "roles")
	return &OIDCIdentity{
		Subject:  strings.TrimSpace(subject),
		Username: strings.TrimSpace(username),
		Email:    strings.TrimSpace(email),
		TeamIDs:  normalizeStringList(teamIDs),
		Scopes:   normalizeStringList(scopeItems),
	}, nil
}

func oidcEnabled() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envOIDCEnabled)))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func parseRSAPublicKeyFromPEM(raw string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("invalid pem")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		cert, certErr := x509.ParseCertificate(block.Bytes)
		if certErr != nil {
			return nil, err
		}
		if cert.PublicKey == nil {
			return nil, errors.New("missing public key")
		}
		if key, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			return key, nil
		}
		return nil, errors.New("public key is not rsa")
	}
	key, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not rsa")
	}
	return key, nil
}

func firstNonEmptyClaim(claims jwt.MapClaims, keys ...string) string {
	for _, key := range keys {
		value, ok := claims[key]
		if !ok {
			continue
		}
		if text := normalizeClaimString(value); text != "" {
			return text
		}
	}
	return ""
}

func collectStringClaimValues(claims jwt.MapClaims, keys ...string) []string {
	out := make([]string, 0)
	for _, key := range keys {
		value, ok := claims[key]
		if !ok || value == nil {
			continue
		}
		for _, item := range flattenClaimStringValues(value) {
			out = append(out, item)
		}
	}
	return normalizeStringList(out)
}

func flattenClaimStringValues(value any) []string {
	switch typed := value.(type) {
	case string:
		text := strings.NewReplacer(",", " ", ";", " ", "\n", " ").Replace(strings.TrimSpace(typed))
		if text == "" {
			return nil
		}
		return strings.Fields(text)
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if normalized := normalizeClaimString(item); normalized != "" {
				out = append(out, normalized)
			}
		}
		return out
	default:
		if normalized := normalizeClaimString(value); normalized != "" {
			return []string{normalized}
		}
		return nil
	}
}

func normalizeClaimString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return ""
	}
}
