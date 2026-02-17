package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type authError struct {
	status  int
	code    string
	message string
}

func (e *authError) Error() string {
	return e.message
}

type tokenClaims struct {
	WorkspaceID string
	AgentName   string
	Scopes      map[string]struct{}
	Exp         int64
}

func authorizeBearer(authHeader, jwtSecret, workspaceID, requiredScope string, now time.Time) (tokenClaims, *authError) {
	claims, err := parseBearer(authHeader, jwtSecret, now)
	if err != nil {
		return tokenClaims{}, err
	}
	if workspaceID != "" && claims.WorkspaceID != workspaceID {
		return tokenClaims{}, &authError{
			status:  403,
			code:    "forbidden",
			message: "workspace mismatch",
		}
	}
	if requiredScope != "" {
		if _, ok := claims.Scopes[requiredScope]; !ok {
			return tokenClaims{}, &authError{
				status:  403,
				code:    "forbidden",
				message: "missing required scope: " + requiredScope,
			}
		}
	}
	return claims, nil
}

func parseBearer(authHeader, jwtSecret string, now time.Time) (tokenClaims, *authError) {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return tokenClaims{}, &authError{
			status:  401,
			code:    "unauthorized",
			message: "missing or invalid bearer token",
		}
	}
	raw := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return tokenClaims{}, &authError{
			status:  401,
			code:    "unauthorized",
			message: "invalid jwt format",
		}
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt header"}
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt header"}
	}
	if header.Alg != "HS256" {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "unsupported jwt algorithm"}
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt payload"}
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt signature"}
	}

	mac := hmac.New(sha256.New, []byte(jwtSecret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expected) {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "jwt signature mismatch"}
	}

	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt payload"}
	}

	workspaceID, ok := payload["workspace_id"].(string)
	if !ok || workspaceID == "" {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "missing workspace_id claim"}
	}
	agentName, ok := payload["agent_name"].(string)
	if !ok || agentName == "" {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "missing agent_name claim"}
	}

	exp, err := parseExp(payload["exp"])
	if err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid exp claim"}
	}
	if now.Unix() >= exp {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "token expired"}
	}
	if aud, ok := payload["aud"].(string); !ok || aud != "relayfile" {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid aud claim"}
	}

	scopes := parseScopes(payload["scopes"])
	if len(scopes) == 0 {
		return tokenClaims{}, &authError{status: 403, code: "forbidden", message: "no scopes granted"}
	}

	return tokenClaims{
		WorkspaceID: workspaceID,
		AgentName:   agentName,
		Scopes:      scopes,
		Exp:         exp,
	}, nil
}

func parseScopes(v any) map[string]struct{} {
	out := map[string]struct{}{}
	switch typed := v.(type) {
	case []any:
		for _, item := range typed {
			if scope, ok := item.(string); ok && scope != "" {
				out[scope] = struct{}{}
			}
		}
	case []string:
		for _, scope := range typed {
			if scope != "" {
				out[scope] = struct{}{}
			}
		}
	case string:
		for _, scope := range strings.Fields(typed) {
			out[scope] = struct{}{}
		}
	}
	return out
}

func parseExp(v any) (int64, error) {
	switch typed := v.(type) {
	case float64:
		return int64(typed), nil
	case int64:
		return typed, nil
	case json.Number:
		return typed.Int64()
	default:
		return 0, errors.New("unsupported exp type")
	}
}

func verifyInternalHMAC(secret, timestamp, signature string, body []byte, now time.Time, maxSkew time.Duration) *authError {
	if timestamp == "" || signature == "" {
		return &authError{status: 401, code: "unauthorized", message: "missing internal auth headers"}
	}
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return &authError{status: 401, code: "unauthorized", message: "invalid internal timestamp"}
	}
	delta := now.Sub(ts)
	if delta < 0 {
		delta = -delta
	}
	if delta > maxSkew {
		return &authError{status: 401, code: "unauthorized", message: "internal request outside replay window"}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(body)
	expectedHex := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.ToLower(signature)), []byte(expectedHex)) {
		return &authError{status: 401, code: "unauthorized", message: "internal signature mismatch"}
	}
	return nil
}
