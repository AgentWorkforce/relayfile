package httpapi

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ParsedPermissionRule represents one ACL rule.
type ParsedPermissionRule struct {
	Effect string // "allow" or "deny"
	Kind   string // "scope", "agent", "workspace", "public"
	Value  string
}

// parsePermissionRule parses a raw rule string like "deny:agent:foo".
func parsePermissionRule(raw string) *ParsedPermissionRule {
	rule := strings.TrimSpace(raw)
	if rule == "" {
		return nil
	}

	effect := "allow"
	lower := strings.ToLower(rule)
	if strings.HasPrefix(lower, "allow:") {
		rule = strings.TrimSpace(rule[len("allow:"):])
	} else if strings.HasPrefix(lower, "deny:") {
		effect = "deny"
		rule = strings.TrimSpace(rule[len("deny:"):])
	}

	normalized := strings.ToLower(rule)
	if normalized == "public" || normalized == "any" || normalized == "*" {
		return &ParsedPermissionRule{
			Effect: effect,
			Kind:   "public",
			Value:  "*",
		}
	}

	parts := strings.Split(rule, ":")
	if len(parts) < 2 {
		return nil
	}
	kind := strings.ToLower(strings.TrimSpace(parts[0]))
	value := strings.TrimSpace(strings.Join(parts[1:], ":"))
	if kind == "" || value == "" {
		return nil
	}
	if kind != "scope" && kind != "agent" && kind != "workspace" {
		return nil
	}

	// Validate rule values to prevent injection of unexpected semantics.
	if !isValidACLRuleValue(kind, value) {
		return nil
	}

	return &ParsedPermissionRule{
		Effect: effect,
		Kind:   kind,
		Value:  value,
	}
}

// aclAgentNamePattern allows alphanumerics, hyphens, underscores, and dots.
var aclAgentNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// aclScopePattern allows scope values like "fs:read", "sync:trigger".
var aclScopePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*(?::[a-zA-Z][a-zA-Z0-9]*)*$`)

// aclWorkspacePattern allows workspace IDs like "ws_123" or UUIDs.
var aclWorkspacePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,255}$`)

// isValidACLRuleValue validates the value for a given rule kind.
func isValidACLRuleValue(kind, value string) bool {
	switch kind {
	case "agent":
		return aclAgentNamePattern.MatchString(value)
	case "scope":
		return aclScopePattern.MatchString(value)
	case "workspace":
		return aclWorkspacePattern.MatchString(value)
	default:
		return false
	}
}

// filePermissionAllows evaluates ACL rules against agent claims.
// Returns true if access is allowed.
func filePermissionAllows(permissions []string, workspaceID string, claims *tokenClaims) bool {
	if len(permissions) == 0 {
		// No ACL policy in effect — allow access.
		return true
	}

	enforceableRuleSeen := false
	allowMatch := false
	for _, raw := range permissions {
		rule := parsePermissionRule(raw)
		if rule == nil {
			// Non-ACL entries (e.g. metadata tags like "role:finance") are
			// ignored — they share the permissions array but are not ACL rules.
			continue
		}
		enforceableRuleSeen = true

		match := false
		switch rule.Kind {
		case "public":
			match = true
		case "scope":
			if claims != nil {
				_, match = claims.Scopes[rule.Value]
			}
		case "agent":
			match = claims != nil && claims.AgentName == rule.Value
		case "workspace":
			match = workspaceID == rule.Value
		}

		if !match {
			continue
		}
		if rule.Effect == "deny" {
			return false
		}
		allowMatch = true
	}

	if allowMatch {
		return true
	}
	// Fail-closed: if enforceable ACL rules exist but none granted access, deny.
	// If no enforceable ACL rules exist (only metadata tags), allow —
	// there is no ACL policy to enforce.
	return !enforceableRuleSeen
}

// resolveFilePermissions walks ancestor dirs to collect ACL rules.
// store is an interface that can read files from the workspace.
func resolveFilePermissions(getFile func(path string) ([]byte, error), path string) []string {
	return resolveFilePermissionsWithTarget(getFile, path, true)
}

func resolveFilePermissionsWithTarget(getFile func(path string) ([]byte, error), path string, includeTarget bool) []string {
	target := normalizeACLPath(path)
	permissions := make([]string, 0)

	for _, dir := range ancestorDirectoriesACL(target) {
		markerPath := joinACLPath(dir, relayfileACLMarkerFile)
		if markerPath == target {
			continue
		}

		marker, err := getFile(markerPath)
		if err != nil || len(marker) == 0 {
			continue
		}

		var rules []string
		if err := json.Unmarshal(marker, &rules); err != nil || len(rules) == 0 {
			continue
		}
		permissions = append(permissions, rules...)
	}

	if includeTarget {
		targetFile, err := getFile(target)
		if err == nil && len(targetFile) > 0 {
			var rules []string
			if err := json.Unmarshal(targetFile, &rules); err == nil && len(rules) > 0 {
				permissions = append(permissions, rules...)
			}
		}
	}

	return permissions
}

const relayfileACLMarkerFile = ".relayfile.acl"

func normalizeACLPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "/"
	}
	prefixed := trimmed
	if !strings.HasPrefix(prefixed, "/") {
		prefixed = "/" + prefixed
	}

	parts := strings.Split(prefixed, "/")
	resolved := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
		default:
			resolved = append(resolved, part)
		}
	}

	result := "/" + strings.Join(resolved, "/")
	if len(result) > 1 {
		result = strings.TrimRight(result, "/")
	}
	return result
}

func joinACLPath(base, child string) string {
	normalizedBase := normalizeACLPath(base)
	if normalizedBase == "/" {
		return normalizeACLPath("/" + child)
	}
	return normalizeACLPath(normalizedBase + "/" + child)
}

func ancestorDirectoriesACL(path string) []string {
	normalized := normalizeACLPath(path)
	parts := strings.Split(normalized, "/")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}

	dirs := []string{"/"}
	current := ""
	limit := len(filtered) - 1
	if limit < 0 {
		limit = 0
	}
	for index := 0; index < limit; index++ {
		if current == "" {
			current = joinACLPath("/", filtered[index])
		} else {
			current = joinACLPath(current, filtered[index])
		}
		dirs = append(dirs, current)
	}
	return dirs
}
