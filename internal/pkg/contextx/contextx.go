package contextx

import (
	"context"
	"log/slog"
)

// contextKey is an unexported type for context keys.
// This prevents collisions with keys defined in other packages.
type contextKey string

const (
	// userIDKey is the context key for storing and retrieving a user's ID.
	userIDKey contextKey = "userID"
	// usernameKey is the context key for storing and retrieving a user's name.
	usernameKey contextKey = "username"
	// accessTokenKey is the context key for storing and retrieving an access token.
	accessTokenKey contextKey = "accessToken"
	// requestIDKey is the context key for storing and retrieving a request identifier.
	requestIDKey contextKey = "requestID"
	// traceIDKey is the context key for storing and retrieving a trace identifier.
	traceIDKey contextKey = "traceID"
	// loggerKey is the context key for storing and retrieving a structured logger.
	loggerKey contextKey = "logger"
	// orchestratorInstanceIDKey stores orchestrator instance identity for job lease ownership.
	orchestratorInstanceIDKey contextKey = "orchestratorInstanceID"
	// triggerTypeKey stores normalized trigger type for run trace linkage.
	triggerTypeKey contextKey = "triggerType"
	// triggerSourceKey stores trigger source for run trace linkage.
	triggerSourceKey contextKey = "triggerSource"
	// triggerInitiatorKey stores trigger initiator for run trace linkage.
	triggerInitiatorKey contextKey = "triggerInitiator"
	// operatorTeamsKey stores caller team scope list resolved by auth middleware.
	operatorTeamsKey contextKey = "operatorTeams"
	// operatorScopesKey stores caller scope list resolved by auth middleware.
	operatorScopesKey contextKey = "operatorScopes"
)

// WithUserID returns a new context with the given user ID.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserID retrieves the user ID from the context.
// Returns an empty string if the user ID is not found.
func UserID(ctx context.Context) string {
	val, ok := ctx.Value(userIDKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithUsername returns a new context with the given username.
func WithUsername(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, usernameKey, username)
}

// Username retrieves the username from the context.
// Returns an empty string if the username is not found.
func Username(ctx context.Context) string {
	val, ok := ctx.Value(usernameKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithAccessToken returns a new context with the given access token.
func WithAccessToken(ctx context.Context, accessToken string) context.Context {
	return context.WithValue(ctx, accessTokenKey, accessToken)
}

// AccessToken retrieves the access token from the context.
// Returns an empty string if the access token is not found.
func AccessToken(ctx context.Context) string {
	val, ok := ctx.Value(accessTokenKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithRequestID returns a new context with the given request ID.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestID retrieves the request ID from the context.
// Returns an empty string if the request ID is not found.
func RequestID(ctx context.Context) string {
	val, ok := ctx.Value(requestIDKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithTraceID returns a new context with the given trace ID.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceID retrieves the trace ID from the context.
// Returns an empty string if the trace ID is not found.
func TraceID(ctx context.Context) string {
	val, ok := ctx.Value(traceIDKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithLogger returns a new context with the given structured logger.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// Logger retrieves the structured logger from the context.
// If no logger is found in the context, it returns slog.Default().
func Logger(ctx context.Context) *slog.Logger {
	val, ok := ctx.Value(loggerKey).(*slog.Logger)
	if !ok || val == nil {
		return slog.Default()
	}
	return val
}

// L is a short alias for Logger, retrieving the structured logger from the context.
// If no logger is found, it returns slog.Default().
func L(ctx context.Context) *slog.Logger {
	return Logger(ctx)
}

// WithOrchestratorInstanceID returns a new context with orchestrator instance id.
func WithOrchestratorInstanceID(ctx context.Context, instanceID string) context.Context {
	return context.WithValue(ctx, orchestratorInstanceIDKey, instanceID)
}

// OrchestratorInstanceID retrieves orchestrator instance id from context.
// Returns empty string when missing.
func OrchestratorInstanceID(ctx context.Context) string {
	val, ok := ctx.Value(orchestratorInstanceIDKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithTriggerType returns a new context with normalized trigger type.
func WithTriggerType(ctx context.Context, triggerType string) context.Context {
	return context.WithValue(ctx, triggerTypeKey, triggerType)
}

// TriggerType retrieves trigger type from context.
func TriggerType(ctx context.Context) string {
	val, ok := ctx.Value(triggerTypeKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithTriggerSource returns a new context with trigger source.
func WithTriggerSource(ctx context.Context, triggerSource string) context.Context {
	return context.WithValue(ctx, triggerSourceKey, triggerSource)
}

// TriggerSource retrieves trigger source from context.
func TriggerSource(ctx context.Context) string {
	val, ok := ctx.Value(triggerSourceKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithTriggerInitiator returns a new context with trigger initiator.
func WithTriggerInitiator(ctx context.Context, triggerInitiator string) context.Context {
	return context.WithValue(ctx, triggerInitiatorKey, triggerInitiator)
}

// TriggerInitiator retrieves trigger initiator from context.
func TriggerInitiator(ctx context.Context) string {
	val, ok := ctx.Value(triggerInitiatorKey).(string)
	if !ok {
		return ""
	}
	return val
}

// WithOperatorTeams returns a new context with operator team scope list.
func WithOperatorTeams(ctx context.Context, teams []string) context.Context {
	if len(teams) == 0 {
		return context.WithValue(ctx, operatorTeamsKey, []string{})
	}
	out := make([]string, 0, len(teams))
	for _, team := range teams {
		if team == "" {
			continue
		}
		out = append(out, team)
	}
	return context.WithValue(ctx, operatorTeamsKey, out)
}

// OperatorTeams retrieves operator team scope list from context.
func OperatorTeams(ctx context.Context) []string {
	val, ok := ctx.Value(operatorTeamsKey).([]string)
	if !ok || len(val) == 0 {
		return nil
	}
	out := make([]string, len(val))
	copy(out, val)
	return out
}

// WithOperatorScopes returns a new context with operator scopes list.
func WithOperatorScopes(ctx context.Context, scopes []string) context.Context {
	if len(scopes) == 0 {
		return context.WithValue(ctx, operatorScopesKey, []string{})
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope == "" {
			continue
		}
		out = append(out, scope)
	}
	return context.WithValue(ctx, operatorScopesKey, out)
}

// OperatorScopes retrieves operator scopes list from context.
func OperatorScopes(ctx context.Context) []string {
	val, ok := ctx.Value(operatorScopesKey).([]string)
	if !ok || len(val) == 0 {
		return nil
	}
	out := make([]string, len(val))
	copy(out, val)
	return out
}
