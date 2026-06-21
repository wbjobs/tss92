package sandbox

import (
	"testing"
)

func TestGenerateAutoTags(t *testing.T) {
	tests := []struct {
		name     string
		stats    *ProfileStats
		expected []string
	}{
		{
			name: "lightweight efficient",
			stats: &ProfileStats{
				CPUTimeMs:       10,
				MemoryPeakBytes: 5 * 1024 * 1024,
				Success:         true,
			},
			expected: []string{"lightweight"},
		},
		{
			name: "high CPU",
			stats: &ProfileStats{
				CPUTimeMs:       600,
				MemoryPeakBytes: 50 * 1024 * 1024,
				Success:         true,
			},
			expected: []string{"high_cpu"},
		},
		{
			name: "memory sensitive",
			stats: &ProfileStats{
				CPUTimeMs:       100,
				MemoryPeakBytes: 150 * 1024 * 1024,
				Success:         true,
			},
			expected: []string{"memory_sensitive"},
		},
		{
			name: "high GC frequency",
			stats: &ProfileStats{
				CPUTimeMs:       100,
				MemoryPeakBytes: 50 * 1024 * 1024,
				GCCount:         10,
				Success:         true,
			},
			expected: []string{"high_gc"},
		},
		{
			name: "execution failed",
			stats: &ProfileStats{
				Success: false,
				Error:   "some error",
			},
			expected: []string{"execution_failed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := GenerateAutoTags(tt.stats)
			if len(tags) != len(tt.expected) {
				t.Errorf("expected %d tags, got %d: %v", len(tt.expected), len(tags), tags)
			}
			for _, expected := range tt.expected {
				found := false
				for _, actual := range tags {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected tag %q not found in %v", expected, tags)
				}
			}
		})
	}
}

func TestParseProfileOutput(t *testing.T) {
	stdout := `Some output
PROFILE_RESULT: 333283335000
PROFILE_STATS: {"cpu_time_ms": 150.5, "wall_time_ms": 250.3, "memory_peak_bytes": 52428800, "gc_count": 2, "gc_time_ms": 10.5, "success": true}
More output`

	stderr := ""

	stats, result, err := ParseProfileOutput(stdout, stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.CPUTimeMs != 150.5 {
		t.Errorf("expected cpu_time_ms 150.5, got %f", stats.CPUTimeMs)
	}
	if stats.MemoryPeakBytes != 52428800 {
		t.Errorf("expected memory_peak_bytes 52428800, got %d", stats.MemoryPeakBytes)
	}
	if stats.GCCount != 2 {
		t.Errorf("expected gc_count 2, got %d", stats.GCCount)
	}
	if stats.GCTimeMs != 10.5 {
		t.Errorf("expected gc_time_ms 10.5, got %f", stats.GCTimeMs)
	}
	if !stats.Success {
		t.Error("expected success true")
	}
	if result != "333283335000" {
		t.Errorf("expected result 333283335000, got %s", result)
	}
}

func TestParseProfileOutputError(t *testing.T) {
	stdout := `PROFILE_STATS: {"success": false, "error": "division by zero"}`
	stderr := `Traceback (most recent call last):
  File "test.py", line 5, in <module>
    x = 1 / 0
ZeroDivisionError: division by zero`

	stats, _, err := ParseProfileOutput(stdout, stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.Success {
		t.Error("expected success false")
	}
	if stats.Error != "division by zero" {
		t.Errorf("expected error 'division by zero', got %q", stats.Error)
	}
}

func TestCountIOOperations(t *testing.T) {
	stderr := `fread: read 100 bytes
write: wrote 200 bytes
pread: another read
fwrite: another write
recv: network read
send: network send`

	reads, writes := CountIOOperations(stderr)
	if reads != 6 {
		t.Errorf("expected 6 reads, got %d", reads)
	}
	if writes != 5 {
		t.Errorf("expected 5 writes, got %d", writes)
	}
}

func TestMergeTags(t *testing.T) {
	userTags := []string{"demo", "test"}
	autoTags := []string{"lightweight", "demo"}

	result := mergeTags(userTags, autoTags)
	tagSet := make(map[string]bool)
	for _, t := range result {
		tagSet[t] = true
	}

	if len(tagSet) != 3 {
		t.Errorf("expected 3 unique tags, got %d", len(tagSet))
	}
	for _, expected := range []string{"demo", "test", "lightweight"} {
		if !tagSet[expected] {
			t.Errorf("expected tag %q missing", expected)
		}
	}
}

func mergeTags(a, b []string) []string {
	set := make(map[string]bool)
	for _, t := range a {
		set[t] = true
	}
	for _, t := range b {
		set[t] = true
	}
	result := make([]string, 0, len(set))
	for t := range set {
		result = append(result, t)
	}
	return result
}
