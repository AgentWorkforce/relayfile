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

// aclScopePattern allows unscoped capability/tag values like "fs:read",
// "sync:trigger", and "finance". Path-bearing filesystem scopes are
// validated separately by isValidACLFilesystemScope.
var aclScopePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*(?::[a-zA-Z][a-zA-Z0-9]*)*$`)

// aclWorkspacePattern allows workspace IDs like "ws_123" or UUIDs.
var aclWorkspacePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,255}$`)

// isValidACLRuleValue validates the value for a given rule kind.
func isValidACLRuleValue(kind, value string) bool {
	switch kind {
	case "agent":
		return aclAgentNamePattern.MatchString(value)
	case "scope":
		return aclScopePattern.MatchString(value) || isValidACLFilesystemScope(value)
	case "workspace":
		return aclWorkspacePattern.MatchString(value)
	default:
		return false
	}
}

type parsedACLFilesystemScope struct {
	action string
	path   string
}

// parseACLFilesystemScope recognizes both the RelayAuth four-segment scope
// vocabulary and Relayfile's legacy workspace-tag vocabulary. The latter is
// still present in durable ACL markers created by Cloud, but no longer needs
// to be carried literally by delegated tokens.
func parseACLFilesystemScope(scope string) (*parsedACLFilesystemScope, bool) {
	segments := strings.SplitN(scope, ":", 4)
	if len(segments) == 2 && segments[0] == "fs" {
		if !isACLFilesystemAction(segments[1]) {
			return nil, false
		}
		return &parsedACLFilesystemScope{action: segments[1], path: "*"}, true
	}
	if len(segments) < 3 {
		return nil, false
	}

	switch segments[0] {
	case "relayfile":
		if segments[1] != "fs" {
			return nil, false
		}
	case "workspace":
		if !aclAgentNamePattern.MatchString(segments[1]) {
			return nil, false
		}
	default:
		return nil, false
	}

	if !isACLFilesystemAction(segments[2]) {
		return nil, false
	}
	path := "*"
	if len(segments) == 4 {
		path = segments[3]
	}
	return &parsedACLFilesystemScope{action: segments[2], path: path}, true
}

func isACLFilesystemAction(action string) bool {
	return action == "read" || action == "write" || action == "manage" || action == "*"
}

func isValidACLFilesystemScope(scope string) bool {
	if strings.TrimSpace(scope) != scope {
		return false
	}
	parsed, ok := parseACLFilesystemScope(scope)
	if !ok {
		return false
	}
	return scopePathValid(parsed.path)
}

// filePermissionAllows evaluates ACL rules against agent claims for one
// filesystem action and path. Scope rules are semantic: a durable rule such
// as relayfile:fs:write:/protected/* matches a delegated token carrying the
// broader relayfile:fs:write:* grant without requiring the rule itself to be
// copied into the token.
// Returns true if access is allowed.
func filePermissionAllows(permissions []string, workspaceID string, claims *tokenClaims, requiredAction, requestedPath string) bool {
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
			match = aclScopeRuleMatches(rule.Value, claims, requiredAction, requestedPath)
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

func aclScopeRuleMatches(scope string, claims *tokenClaims, requiredAction, requestedPath string) bool {
	if claims == nil {
		return false
	}

	parsed, filesystemScope := parseACLFilesystemScope(scope)
	if !filesystemScope {
		_, exactMatch := claims.Scopes[scope]
		return exactMatch
	}
	if !scopeActionMatches(parsed.action, requiredAction) {
		return false
	}
	if parsed.path != "*" && !scopePathMatches(parsed.path, requestedPath) {
		return false
	}

	return scopeMatchesPath(claims.Scopes, "fs:"+requiredAction, requestedPath)
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
