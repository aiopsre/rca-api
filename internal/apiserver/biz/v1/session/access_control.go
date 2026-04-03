package session

import (
	"context"
	"strings"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
)

const operatorTeamWildcard = "*"

var supportedScopePrefixes = map[string]struct{}{
	"ns":        {},
	"namespace": {},
	"tenant":    {},
	"team":      {},
}

// CanOperatorAccessSession returns whether current caller can access one session.
// Rule (minimal): assignee self OR matching team scope; non-operator context is allowed.
func CanOperatorAccessSession(
	ctx context.Context,
	sessionObj *model.SessionContextM,
	incidentObj *model.IncidentM,
	assignee string,
) bool {
	if sessionObj == nil {
		return false
	}
	operatorID := normalizeLower(contextx.UserID(ctx))
	if operatorID == "" {
		// Keep compatibility for internal/system calls without operator identity.
		return true
	}
	username := normalizeLower(contextx.Username(ctx))
	if operatorMatchesAssignee(operatorID, username, assignee) {
		return true
	}
	teamSet := buildOperatorTeamSet(contextx.OperatorTeams(ctx))
	if len(teamSet) == 0 {
		return false
	}
	if _, ok := teamSet[operatorTeamWildcard]; ok {
		return true
	}
	for _, scope := range buildSessionScopeTokens(sessionObj, incidentObj) {
		if _, ok := teamSet[scope]; ok {
			return true
		}
	}
	return false
}

func operatorMatchesAssignee(operatorID string, username string, assignee string) bool {
	assignee = normalizeLower(assignee)
	if assignee == "" {
		return false
	}
	identities := buildOperatorIdentitySet(operatorID, username)
	if len(identities) == 0 {
		return false
	}
	_, ok := identities[assignee]
	return ok
}

func buildOperatorIdentitySet(operatorID string, username string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range []string{operatorID, username} {
		value = normalizeLower(value)
		if value == "" {
			continue
		}
		out[value] = struct{}{}
		if strings.HasPrefix(value, "user:") {
			base := strings.TrimPrefix(value, "user:")
			if base != "" {
				out[base] = struct{}{}
			}
		} else {
			out["user:"+value] = struct{}{}
		}
	}
	return out
}

func buildOperatorTeamSet(teamIDs []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, raw := range teamIDs {
		team := normalizeLower(raw)
		if team == "" {
			continue
		}
		if team == operatorTeamWildcard {
			out[operatorTeamWildcard] = struct{}{}
			continue
		}
		addScopeToken(out, team)
		prefix, value, ok := splitScopeToken(team)
		if ok {
			addScopeByPrefix(out, prefix, value)
			continue
		}
		// Accept raw team ids and match them against common scope forms.
		addScopeByPrefix(out, "namespace", team)
		addScopeByPrefix(out, "team", team)
		addScopeByPrefix(out, "tenant", team)
	}
	return out
}

func buildSessionScopeTokens(sessionObj *model.SessionContextM, incidentObj *model.IncidentM) []string {
	if sessionObj == nil {
		return nil
	}
	set := map[string]struct{}{}
	if incidentObj != nil {
		if ns := normalizeLower(incidentObj.Namespace); ns != "" {
			addScopeByPrefix(set, "namespace", ns)
		}
		if tenant := normalizeLower(incidentObj.TenantID); tenant != "" {
			addScopeByPrefix(set, "tenant", tenant)
		}
	}
	businessKey := strings.TrimSpace(sessionObj.BusinessKey)
	if businessKey != "" {
		parts := strings.Split(strings.ToLower(businessKey), ":")
		for idx := 0; idx+1 < len(parts); idx++ {
			prefix := strings.TrimSpace(parts[idx])
			value := strings.TrimSpace(parts[idx+1])
			if _, ok := supportedScopePrefixes[prefix]; !ok || value == "" {
				continue
			}
			addScopeByPrefix(set, prefix, value)
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

func addScopeByPrefix(set map[string]struct{}, prefix string, value string) {
	value = normalizeLower(value)
	if value == "" {
		return
	}
	switch prefix {
	case "ns", "namespace":
		addScopeToken(set, value)
		addScopeToken(set, "ns:"+value)
		addScopeToken(set, "namespace:"+value)
	case "team":
		addScopeToken(set, value)
		addScopeToken(set, "team:"+value)
	case "tenant":
		addScopeToken(set, value)
		addScopeToken(set, "tenant:"+value)
	default:
		addScopeToken(set, prefix+":"+value)
	}
}

func addScopeToken(set map[string]struct{}, token string) {
	token = normalizeLower(token)
	if token == "" {
		return
	}
	set[token] = struct{}{}
}

func splitScopeToken(token string) (string, string, bool) {
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	prefix := normalizeLower(parts[0])
	value := normalizeLower(parts[1])
	if _, ok := supportedScopePrefixes[prefix]; !ok {
		return "", "", false
	}
	if value == "" {
		return "", "", false
	}
	return prefix, value, true
}

func normalizeLower(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
