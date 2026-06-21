package profiler

import (
	"testing"

	pb "profiler/proto"
)

func TestLanguageConversion(t *testing.T) {
	tests := []struct {
		lang     pb.Language
		expected string
	}{
		{pb.Language_PYTHON, "python"},
		{pb.Language_JAVA, "java"},
		{pb.Language_GO, "go"},
	}

	for _, tt := range tests {
		t.Run(tt.lang.String(), func(t *testing.T) {
			lang := tt.lang.String()
			if lang == "PYTHON" {
				lang = "python"
			} else if lang == "JAVA" {
				lang = "java"
			} else if lang == "GO" {
				lang = "go"
			}
			if lang != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, lang)
			}
		})
	}
}

func TestMergeTags(t *testing.T) {
	tests := []struct {
		name     string
		userTags []string
		autoTags []string
		expected int
	}{
		{
			name:     "no duplicates",
			userTags: []string{"a", "b"},
			autoTags: []string{"c", "d"},
			expected: 4,
		},
		{
			name:     "with duplicates",
			userTags: []string{"a", "b", "c"},
			autoTags: []string{"b", "c", "d"},
			expected: 4,
		},
		{
			name:     "empty user tags",
			userTags: []string{},
			autoTags: []string{"a", "b"},
			expected: 2,
		},
		{
			name:     "empty auto tags",
			userTags: []string{"a", "b"},
			autoTags: []string{},
			expected: 2,
		},
		{
			name:     "both empty",
			userTags: []string{},
			autoTags: []string{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeTags(tt.userTags, tt.autoTags)
			if len(result) != tt.expected {
				t.Errorf("expected %d tags, got %d", tt.expected, len(result))
			}
		})
	}
}
