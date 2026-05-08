package mountfuse

import "testing"

func TestParseNameID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		basename      string
		wantName      string
		wantID        string
		wantRoundTrip string
	}{
		{
			name:          "new style",
			basename:      "alpha__123e4567.json",
			wantName:      "alpha",
			wantID:        "123e4567",
			wantRoundTrip: "alpha__123e4567.json",
		},
		{
			name:     "legacy",
			basename: "uuid.json",
			wantName: "",
			wantID:   "uuid",
		},
		{
			name:     "no extension",
			basename: "alpha__123e4567",
			wantName: "alpha",
			wantID:   "123e4567",
		},
		{
			name:     "multi separator",
			basename: "a__b__c.json",
			wantName: "a__b",
			wantID:   "c",
		},
		{
			name:     "empty input",
			basename: "",
			wantName: "",
			wantID:   "",
		},
		{
			name:     "root path",
			basename: "/",
			wantName: "",
			wantID:   "",
		},
		{
			name:     "directory style basename",
			basename: "thread__01HXYZ",
			wantName: "thread",
			wantID:   "01HXYZ",
		},
		{
			name:     "dot only extension",
			basename: ".json",
			wantName: "",
			wantID:   "",
		},
		{
			name:     "empty name half",
			basename: "__abc.json",
			wantName: "",
			wantID:   "abc",
		},
		{
			name:     "slug with dot",
			basename: "Re_Welcome.draft__01HXYZ.json",
			wantName: "Re_Welcome.draft",
			wantID:   "01HXYZ",
		},
		{
			name:     "mixed case extension",
			basename: "alpha__ABC.JSON",
			wantName: "alpha",
			wantID:   "ABC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotID := ParseNameID(tt.basename)
			if gotName != tt.wantName || gotID != tt.wantID {
				t.Fatalf("ParseNameID(%q) = (%q, %q), want (%q, %q)", tt.basename, gotName, gotID, tt.wantName, tt.wantID)
			}
			if got := IDFromBasename(tt.basename); got != tt.wantID {
				t.Fatalf("IDFromBasename(%q) = %q, want %q", tt.basename, got, tt.wantID)
			}
			if tt.wantRoundTrip != "" {
				if got := nameWithId(gotName, gotID); got != tt.wantRoundTrip {
					t.Fatalf("nameWithId(%q, %q) = %q, want %q", gotName, gotID, got, tt.wantRoundTrip)
				}
			}
		})
	}
}

func TestNameIDSameIDAcrossNamingStyles(t *testing.T) {
	t.Parallel()

	names := []string{
		"abc.json",
		"slug__abc.json",
		"a__b__abc.json",
	}
	for _, name := range names {
		if got := IDFromBasename(name); got != "abc" {
			t.Fatalf("IDFromBasename(%q) = %q, want %q", name, got, "abc")
		}
	}
}

func TestResolveNameByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		names         []string
		requestedName string
		wantName      string
		wantOK        bool
	}{
		{
			name:          "legacy alias resolves canonical when only canonical exists",
			names:         []string{"only-human__shared-id.json"},
			requestedName: "shared-id.json",
			wantName:      "only-human__shared-id.json",
			wantOK:        true,
		},
		{
			name:          "canonical sibling wins when legacy and canonical siblings coexist",
			names:         []string{"legacy-id.json", "human-name__legacy-id.json"},
			requestedName: "legacy-id.json",
			wantName:      "human-name__legacy-id.json",
			wantOK:        true,
		},
		{
			name:          "canonical exact match is stable",
			names:         []string{"legacy-id.json", "human-name__legacy-id.json"},
			requestedName: "human-name__legacy-id.json",
			wantName:      "human-name__legacy-id.json",
			wantOK:        true,
		},
		{
			name:          "missing id returns false",
			names:         []string{"alpha__123.json"},
			requestedName: "missing.json",
			wantName:      "",
			wantOK:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotOK := resolveNameByID(tt.names, tt.requestedName)
			if gotName != tt.wantName || gotOK != tt.wantOK {
				t.Fatalf("resolveNameByID(%v, %q) = (%q, %t), want (%q, %t)", tt.names, tt.requestedName, gotName, gotOK, tt.wantName, tt.wantOK)
			}
		})
	}
}
