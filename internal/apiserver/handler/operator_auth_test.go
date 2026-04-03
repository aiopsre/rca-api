package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func TestOperatorAuth_LoginAndTokenGuard(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}, &model.SessionHistoryEventM{}))

	token := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "operator:test-a",
		"team_ids":    []string{"default"},
		"scopes":      []string{"ai.read", "ai.run"},
	})
	require.NotEmpty(t, token)

	status, _, err := doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/inbox", baseURL), nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/inbox", baseURL), nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, status)

	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/team_dashboard", baseURL), nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, status)

	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/assignment_history", baseURL), nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, status)
}

func TestOperatorAuth_RefreshTokenLifecycle(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}, &model.SessionHistoryEventM{}))

	loginPayload := map[string]any{
		"operator_id": "operator:test-refresh",
		"team_ids":    []string{"namespace:payments"},
		"scopes":      []string{"ai.read", "ai.run"},
	}
	status, loginBody, err := doJSONRequestWithHeaders(client, http.MethodPost, fmt.Sprintf("%s/v1/auth/login", baseURL), mustJSON(t, loginPayload), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	loginData := extractDataContainer(loginBody)
	accessToken := extractString(loginData, "token", "access_token")
	refreshToken := extractString(loginData, "refresh_token", "refreshToken")
	require.NotEmpty(t, accessToken)
	require.NotEmpty(t, refreshToken)

	status, refreshBody, err := doJSONRequestWithHeaders(client, http.MethodPost, fmt.Sprintf("%s/v1/auth/refresh", baseURL), mustJSON(t, map[string]any{
		"refresh_token": refreshToken,
	}), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	refreshData := extractDataContainer(refreshBody)
	refreshedAccessToken := extractString(refreshData, "token", "access_token")
	refreshedRefreshToken := extractString(refreshData, "refresh_token", "refreshToken")
	require.NotEmpty(t, refreshedAccessToken)
	require.NotEmpty(t, refreshedRefreshToken)
	require.NotEqual(t, accessToken, refreshedAccessToken)
	require.NotEqual(t, refreshToken, refreshedRefreshToken)

	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/inbox", baseURL), nil, map[string]string{
		"Authorization": "Bearer " + refreshedAccessToken,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/inbox", baseURL), nil, map[string]string{
		"Authorization": "Bearer " + refreshToken,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, status)
}

func TestOperatorAuth_LoginWithOIDCIDToken(t *testing.T) {
	t.Setenv("RCA_API_AUTH_OIDC_ENABLED", "true")
	t.Setenv("RCA_API_AUTH_OIDC_ISSUER", "https://issuer.rca.test")
	t.Setenv("RCA_API_AUTH_OIDC_AUDIENCE", "rca-operator-ui")
	t.Setenv("RCA_API_AUTH_OIDC_HS256_SECRET", "oidc-test-secret")
	t.Setenv("RCA_API_AUTH_OIDC_RS256_PUBLIC_KEY_PEM", "")

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	now := time.Now().UTC()
	idToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":                "https://issuer.rca.test",
		"aud":                "rca-operator-ui",
		"sub":                "oidc-user-a",
		"preferred_username": "alice-oidc",
		"team_ids":           []any{"namespace:payments"},
		"scope":              "ai.read ai.run",
		"exp":                now.Add(30 * time.Minute).Unix(),
		"iat":                now.Unix(),
		"nbf":                now.Add(-1 * time.Minute).Unix(),
	})
	signed, err := idToken.SignedString([]byte("oidc-test-secret"))
	require.NoError(t, err)

	status, body, err := doJSONRequestWithHeaders(client, http.MethodPost, fmt.Sprintf("%s/v1/auth/login", baseURL), mustJSON(t, map[string]any{
		"oidc_id_token": signed,
	}), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	data := extractDataContainer(body)
	require.NotEmpty(t, extractString(data, "token", "access_token"))
	require.NotEmpty(t, extractString(data, "refresh_token"))
	operator, ok := data["operator"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "oidc-user-a", extractString(operator, "operator_id", "operatorId"))
}

func TestOperatorAudit_SensitiveActionWritesIncidentActionLog(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()

	require.NoError(t, s.DB(context.Background()).AutoMigrate(
		&model.SessionContextM{},
		&model.SessionHistoryEventM{},
		&model.IncidentActionLogM{},
	))
	incident := createAIJobLongPollTestIncident(t, s)
	incident.Namespace = "payments"
	require.NoError(t, s.Incident().Update(context.Background(), incident))
	sessionResp, err := biz.NewBiz(s).SessionV1().EnsureIncidentSession(context.Background(), &sessionbiz.EnsureIncidentSessionRequest{
		IncidentID: incident.IncidentID,
	})
	require.NoError(t, err)
	require.NotNil(t, sessionResp)
	require.NotNil(t, sessionResp.Session)
	sessionID := strings.TrimSpace(sessionResp.Session.SessionID)
	require.NotEmpty(t, sessionID)
	token := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "operator:audit",
		"team_ids":    []string{"namespace:payments"},
		"scopes":      []string{"session.assignment", "ai.read"},
	})

	status, body, err := doJSONRequestWithHeaders(client, http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/assign", baseURL, sessionID),
		mustJSON(t, map[string]any{
			"assignee": "user:oncall-audit",
			"note":     "assign for audit trail",
		}),
		map[string]string{"Authorization": "Bearer " + token},
	)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, status, "body=%s", string(body))

	total, rows, err := s.IncidentActionLog().List(context.Background(), where.T(context.Background()).F("incident_id", incident.IncidentID))
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, int64(1))
	found := false
	for _, row := range rows {
		if row == nil {
			continue
		}
		if strings.TrimSpace(row.ActionType) == "operator_api_call" {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestOperatorAuth_SessionAccessControlByTeamOrSelf(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()

	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}, &model.SessionHistoryEventM{}))
	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionBiz := biz.NewBiz(s).SessionV1()

	incidentA := createAIJobLongPollTestIncident(t, s)
	incidentB := createAIJobLongPollTestIncident(t, s)
	incidentA.Namespace = "payments"
	require.NoError(t, s.Incident().Update(context.Background(), incidentA))
	incidentB.Namespace = "checkout"
	require.NoError(t, s.Incident().Update(context.Background(), incidentB))

	jobA := createFinalizedTraceJob(t, aiBiz, incidentA.IncidentID, "manual", "manual_api", "user:a", buildDiagnosisJSON(
		"payments timeout",
		"dependency_timeout",
		"dependency",
		0.71,
		"ev-a-1",
		"ev-a-2",
	))
	jobB := createFinalizedTraceJob(t, aiBiz, incidentB.IncidentID, "manual", "manual_api", "user:b", buildDiagnosisJSON(
		"checkout memory pressure",
		"resource_pressure",
		"app",
		0.66,
		"ev-b-1",
		"ev-b-2",
	))
	sessionA := mustHandlerSessionIDByJob(t, s, jobA)
	sessionB := mustHandlerSessionIDByJob(t, s, jobB)

	_, err := sessionBiz.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionB,
		Assignee:   "user:oncall-b",
		AssignedBy: strPtr("user:lead"),
	})
	require.NoError(t, err)

	teamToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "operator:team-payments",
		"team_ids":    []string{"namespace:payments"},
		"scopes":      []string{"ai.read", "ai.run"},
	})
	status, _, err := doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/workbench", baseURL, sessionA), nil, map[string]string{
		"Authorization": "Bearer " + teamToken,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/workbench", baseURL, sessionB), nil, map[string]string{
		"Authorization": "Bearer " + teamToken,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status)

	selfToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "oncall-b",
		"scopes":      []string{"ai.read", "ai.run"},
	})
	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/workbench", baseURL, sessionB), nil, map[string]string{
		"Authorization": "Bearer " + selfToken,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
}

func mustLoginOperatorToken(t *testing.T, client *http.Client, baseURL string, body map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	status, respBody, err := doJSONRequestWithHeaders(client, http.MethodPost, fmt.Sprintf("%s/v1/auth/login", baseURL), payload, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	data := extractDataContainer(respBody)
	token := extractString(data, "token", "Token")
	require.NotEmpty(t, token)
	return token
}

func doJSONRequestWithHeaders(
	client *http.Client,
	method string,
	url string,
	payload []byte,
	headers map[string]string,
) (status int, body []byte, err error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

func mustJSON(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return raw
}
