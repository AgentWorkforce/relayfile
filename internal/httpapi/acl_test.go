package httpapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func TestParsePermissionRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want *ParsedPermissionRule
	}{
		{
			name: "deny agent",
			raw:  "deny:agent:code-agent",
			want: &ParsedPermissionRule{Effect: "deny", Kind: "agent", Value: "code-agent"},
		},
		{
			name: "allow scope",
			raw:  "allow:scope:fs:read",
			want: &ParsedPermissionRule{Effect: "allow", Kind: "scope", Value: "fs:read"},
		},
		{
			name: "public",
			raw:  "public",
			want: &ParsedPermissionRule{Effect: "allow", Kind: "public", Value: "*"},
		},
		{
			name: "deny workspace",
			raw:  "deny:workspace:ws_123",
			want: &ParsedPermissionRule{Effect: "deny", Kind: "workspace", Value: "ws_123"},
		},
		{
			name: "empty",
			raw:  "",
		},
		{
			name: "invalid",
			raw:  "invalid",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parsePermissionRule(tt.raw)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("expected %+v, got %+v", tt.want, got)
			}
		})
	}
}

func TestFilePermissionAllows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		permissions []string
		workspaceID string
		claims      tokenClaims
		want        bool
	}{
		{
			name:        "no rules defaults open",
			permissions: nil,
			workspaceID: "ws_123",
			claims:      tokenClaims{},
			want:        true,
		},
		{
			name:        "matching deny agent rejects",
			permissions: []string{"deny:agent:code-agent"},
			workspaceID: "ws_123",
			claims: tokenClaims{
				AgentName: "code-agent",
			},
			want: false,
		},
		{
			name:        "non matching deny falls through to allow",
			permissions: []string{"deny:agent:code-agent", "public"},
			workspaceID: "ws_123",
			claims: tokenClaims{
				AgentName: "other-agent",
			},
			want: true,
		},
		{
			name:        "matching scope allows",
			permissions: []string{"allow:scope:fs:read"},
			workspaceID: "ws_123",
			claims: tokenClaims{
				Scopes: map[string]struct{}{"fs:read": {}},
			},
			want: true,
		},
		{
			name:        "deny overrides allow",
			permissions: []string{"allow:agent:code-agent", "deny:agent:code-agent"},
			workspaceID: "ws_123",
			claims: tokenClaims{
				AgentName: "code-agent",
			},
			want: false,
		},
		{
			name:        "public allows everyone",
			permissions: []string{"public"},
			workspaceID: "ws_123",
			claims:      tokenClaims{},
			want:        true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := filePermissionAllows(tt.permissions, tt.workspaceID, &tt.claims)
			if got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestResolveFilePermissions(t *testing.T) {
	t.Parallel()

	t.Run("inherits root and nested markers", func(t *testing.T) {
		t.Parallel()

		store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
		t.Cleanup(store.Close)

		writes := []relayfile.WriteRequest{
			{
				WorkspaceID: "ws_acl_nested",
				Path:        "/.relayfile.acl",
				IfMatch:     "0",
				ContentType: "text/plain",
				Content:     "root marker",
				Semantics: relayfile.FileSemantics{
					Permissions: []string{"scope:root"},
				},
				CorrelationID: "corr_acl_root",
			},
			{
				WorkspaceID: "ws_acl_nested",
				Path:        "/src/.relayfile.acl",
				IfMatch:     "0",
				ContentType: "text/plain",
				Content:     "src marker",
				Semantics: relayfile.FileSemantics{
					Permissions: []string{"deny:agent:blocked"},
				},
				CorrelationID: "corr_acl_src",
			},
			{
				WorkspaceID:   "ws_acl_nested",
				Path:          "/src/file.ts",
				IfMatch:       "0",
				ContentType:   "text/plain",
				Content:       "console.log('ok')",
				CorrelationID: "corr_acl_file",
			},
		}

		for _, write := range writes {
			if _, err := store.WriteFile(write); err != nil {
				t.Fatalf("write %s failed: %v", write.Path, err)
			}
		}

		got := resolveFilePermissions(func(path string) ([]byte, error) {
			file, err := store.ReadFile("ws_acl_nested", path)
			if err != nil || len(file.Semantics.Permissions) == 0 {
				return nil, err
			}
			return json.Marshal(file.Semantics.Permissions)
		}, "/src/file.ts")
		want := []string{"scope:root", "deny:agent:blocked"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("expected %v, got %v", want, got)
		}
	})

	t.Run("no markers returns empty rules", func(t *testing.T) {
		t.Parallel()

		store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
		t.Cleanup(store.Close)

		if _, err := store.WriteFile(relayfile.WriteRequest{
			WorkspaceID:   "ws_acl_empty",
			Path:          "/src/file.ts",
			IfMatch:       "0",
			ContentType:   "text/plain",
			Content:       "console.log('ok')",
			CorrelationID: "corr_acl_empty",
		}); err != nil {
			t.Fatalf("write file failed: %v", err)
		}

		got := resolveFilePermissions(func(path string) ([]byte, error) {
			file, err := store.ReadFile("ws_acl_empty", path)
			if err != nil || len(file.Semantics.Permissions) == 0 {
				return nil, err
			}
			return json.Marshal(file.Semantics.Permissions)
		}, "/src/file.ts")
		if len(got) != 0 {
			t.Fatalf("expected no rules, got %v", got)
		}
	})
}
