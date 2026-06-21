package comparator

import (
	"strings"
	"testing"
	"time"

	"profiler/internal/storage"
	pb "profiler/proto"
)

func TestCalculateMetricDiffs(t *testing.T) {
	oldRecord := &storage.ProfileRecord{
		CPUTimeMs:      100,
		MemoryPeakMB:   50,
		IOReadCount:    10,
		IOWriteCount:   5,
		GCCount:        2,
		GCTimeMs:       10,
		ExecutionTimeMs: 110,
	}

	newRecord := &storage.ProfileRecord{
		CPUTimeMs:      115,
		MemoryPeakMB:   45,
		IOReadCount:    10,
		IOWriteCount:   5,
		GCCount:        2,
		GCTimeMs:       10,
		ExecutionTimeMs: 126.5,
	}

	diffs := calculateMetricDiffs(oldRecord, newRecord, 10.0)

	cpuDiff := findDiff(diffs, "cpu_time_ms")
	if cpuDiff == nil {
		t.Fatal("cpu_time_ms diff not found")
	}
	if cpuDiff.AbsoluteDiff != 15.0 {
		t.Errorf("expected absolute diff 15.0, got %f", cpuDiff.AbsoluteDiff)
	}
	if cpuDiff.PercentageDiff != 15.0 {
		t.Errorf("expected percentage diff 15.0, got %f", cpuDiff.PercentageDiff)
	}
	if cpuDiff.ChangeType != "regression" {
		t.Errorf("expected change_type regression, got %s", cpuDiff.ChangeType)
	}
	if !cpuDiff.IsSignificant {
		t.Error("expected is_significant true for 15% change")
	}

	memDiff := findDiff(diffs, "memory_peak_mb")
	if memDiff == nil {
		t.Fatal("memory_peak_mb diff not found")
	}
	if memDiff.ChangeType != "improvement" {
		t.Errorf("expected change_type improvement for memory, got %s", memDiff.ChangeType)
	}
}

func findDiff(diffs []*pb.MetricDiff, name string) *pb.MetricDiff {
	for _, d := range diffs {
		if d.GetMetricName() == name {
			return d
		}
	}
	return nil
}

func TestCalculateMetricDiffsZeroOldValue(t *testing.T) {
	oldRecord := &storage.ProfileRecord{
		CPUTimeMs:      100,
		MemoryPeakMB:   0,
		GCCount:        0,
	}

	newRecord := &storage.ProfileRecord{
		CPUTimeMs:      100,
		MemoryPeakMB:   50,
		GCCount:        0,
	}

	diffs := calculateMetricDiffs(oldRecord, newRecord, 10.0)

	memDiff := findDiff(diffs, "memory_peak_mb")
	if memDiff == nil {
		t.Fatal("memory_peak_mb diff not found")
	}
	if memDiff.PercentageDiff != 100 {
		t.Errorf("expected 100%% increase from 0, got %f", memDiff.PercentageDiff)
	}
}

func TestCalculateSourceDiffs(t *testing.T) {
	oldSource := `def profile_target():
    s = 0
    for i in range(1000):
        s += i * i
    return s`

	newSource := `def profile_target():
    s = 0
    for i in range(2000):
        s += i * i
    print(s)
    return s`

	diffs := calculateSourceDiffs(oldSource, newSource, nil)

	hasAdded := false
	hasChanged := false
	for _, d := range diffs {
		if d.LineType == "added" {
			hasAdded = true
		}
		if d.LineType != "unchanged" {
			hasChanged = true
		}
	}

	if !hasAdded {
		t.Error("expected at least one added line")
	}
	if !hasChanged {
		t.Error("expected at least one changed line")
	}
}

func TestPerformanceImpactCalculation(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		lineType string
		minScore float64
		maxScore float64
	}{
		{
			name:     "loop with allocation",
			line:     "for i in range(1000):\n    arr.append(i*i)",
			lineType: "added",
			minScore: 40,
			maxScore: 200,
		},
		{
			name:     "io operation",
			line:     "with open('file.txt') as f:\n    data = f.read()",
			lineType: "added",
			minScore: 40,
			maxScore: 150,
		},
		{
			name:     "simple line",
			line:     "x = a + b",
			lineType: "added",
			minScore: 5,
			maxScore: 50,
		},
		{
			name:     "removed loop",
			line:     "for i in range(100):\n    process(i)",
			lineType: "removed",
			minScore: -200,
			maxScore: -10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := calculatePerformanceImpact("", tt.line, tt.lineType, 0, 0)
			if score < tt.minScore || score > tt.maxScore {
				t.Errorf("score %.1f out of expected range [%.1f, %.1f]", score, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestAnalyzeTrend(t *testing.T) {
	cfg := DefaultCompareConfig()

	t.Run("regression", func(t *testing.T) {
		diffs := []*pb.MetricDiff{
			{
				MetricName:    "cpu_time_ms",
				OldValue:      100,
				NewValue:      150,
				AbsoluteDiff:  50,
				PercentageDiff: 50,
				ChangeType:    "regression",
				IsSignificant: true,
			},
		}
		trend, regressions, improvements := analyzeTrend(diffs, cfg)
		if trend != "regressed" {
			t.Errorf("expected trend regressed, got %s", trend)
		}
		if len(regressions) == 0 {
			t.Error("expected at least one regression tag")
		}
		if len(improvements) > 0 {
			t.Error("expected no improvement tags")
		}
	})

	t.Run("improvement", func(t *testing.T) {
		diffs := []*pb.MetricDiff{
			{
				MetricName:     "cpu_time_ms",
				OldValue:       150,
				NewValue:       100,
				AbsoluteDiff:   -50,
				PercentageDiff: -33.33,
				ChangeType:     "improvement",
				IsSignificant:  true,
			},
		}
		trend, regressions, improvements := analyzeTrend(diffs, cfg)
		if trend != "improved" {
			t.Errorf("expected trend improved, got %s", trend)
		}
		if len(improvements) == 0 {
			t.Error("expected at least one improvement tag")
		}
		if len(regressions) > 0 {
			t.Error("expected no regression tags")
		}
	})

	t.Run("stable", func(t *testing.T) {
		diffs := []*pb.MetricDiff{
			{
				MetricName:     "cpu_time_ms",
				OldValue:       100,
				NewValue:       105,
				AbsoluteDiff:   5,
				PercentageDiff: 5,
				ChangeType:     "regression",
				IsSignificant:  false,
			},
		}
		trend, _, _ := analyzeTrend(diffs, cfg)
		if trend != "stable" {
			t.Errorf("expected trend stable, got %s", trend)
		}
	})
}

func TestGenerateSummary(t *testing.T) {
	oldRecord := &storage.ProfileRecord{
		CPUTimeMs:    100,
		MemoryPeakMB: 50,
		Timestamp:    time.Now().Add(-24 * time.Hour),
	}

	newRecord := &storage.ProfileRecord{
		CPUTimeMs:    115,
		MemoryPeakMB: 50,
		Timestamp:    time.Now(),
	}

	t.Run("significant change", func(t *testing.T) {
		diffs := []*pb.MetricDiff{
			{
				MetricName:     "cpu_time_ms",
				OldValue:       100,
				NewValue:       115,
				PercentageDiff: 15,
				ChangeType:     "regression",
				IsSignificant:  true,
			},
		}
		summary := generateSummary(oldRecord, newRecord, diffs, "regressed")
		if summary == "" {
			t.Error("summary should not be empty")
		}
		if !contains(summary, "15.0%") {
			t.Errorf("summary should mention 15%% change, got: %s", summary)
		}
	})

	t.Run("stable", func(t *testing.T) {
		diffs := []*pb.MetricDiff{
			{
				MetricName:     "cpu_time_ms",
				OldValue:       100,
				NewValue:       105,
				PercentageDiff: 5,
				ChangeType:     "regression",
				IsSignificant:  false,
			},
		}
		summary := generateSummary(oldRecord, newRecord, diffs, "stable")
		if !contains(summary, "stable") {
			t.Errorf("summary should mention stable, got: %s", summary)
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (strings.EqualFold(s, substr) || len(s) > 0 && containsHelper(strings.ToLower(s), strings.ToLower(substr)))
}

func containsHelper(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestLCSDiff(t *testing.T) {
	oldLines := []string{"a", "b", "c", "d"}
	newLines := []string{"a", "x", "c", "y"}

	changes := lcsDiff(oldLines, newLines)

	addedCount := 0
	removedCount := 0
	for _, ch := range changes {
		switch ch.lineType {
		case "added":
			addedCount++
		case "removed":
			removedCount++
		}
	}

	if addedCount != 2 {
		t.Errorf("expected 2 added lines, got %d", addedCount)
	}
	if removedCount != 2 {
		t.Errorf("expected 2 removed lines, got %d", removedCount)
	}
}

func TestNormalizeLine(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"  hello   world  ", "hello world"},
		{"\tfoo\t\tbar\n", "foo bar"},
		{"  for   i   in   range(10)  ", "for i in range(10)"},
	}

	for _, tt := range tests {
		result := normalizeLine(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeLine(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestUniqueStrings(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b"}
	result := uniqueStrings(input)

	if len(result) != 3 {
		t.Errorf("expected 3 unique strings, got %d", len(result))
	}

	seen := make(map[string]bool)
	for _, s := range result {
		if seen[s] {
			t.Errorf("duplicate string %q in result", s)
		}
		seen[s] = true
	}
}

func TestCompareProfilesIntegration(t *testing.T) {
	oldRecord := &storage.ProfileRecord{
		ProfileID:     "old-123",
		Language:      "python",
		FunctionName:  "test_func",
		CPUTimeMs:     100,
		MemoryPeakMB:  50,
		IOReadCount:   10,
		IOWriteCount:  5,
		GCCount:       2,
		GCTimeMs:      10,
		ExecutionTimeMs: 110,
		Success:       true,
		Tags:          []string{"lightweight"},
		Timestamp:     time.Now().Add(-1 * time.Hour),
	}

	newRecord := &storage.ProfileRecord{
		ProfileID:     "new-456",
		Language:      "python",
		FunctionName:  "test_func",
		CPUTimeMs:     130,
		MemoryPeakMB:  48,
		IOReadCount:   10,
		IOWriteCount:  5,
		GCCount:       2,
		GCTimeMs:      10,
		ExecutionTimeMs: 143,
		Success:       true,
		Tags:          []string{"high_cpu"},
		Timestamp:     time.Now(),
	}

	oldSource := `def profile_target():
    s = 0
    for i in range(1000):
        s += i * i
    return s`

	newSource := `def profile_target():
    s = 0
    for i in range(1000):
        s += i * i
    s = s * 2
    return s`

	result, err := CompareProfiles(oldRecord, newRecord, oldSource, newSource, nil)
	if err != nil {
		t.Fatalf("CompareProfiles failed: %v", err)
	}

	if result.TimeDeltaSec <= 0 {
		t.Error("expected positive time delta")
	}

	if result.PerformanceTrend != "regressed" {
		t.Errorf("expected trend regressed, got %s", result.PerformanceTrend)
	}

	if len(result.MetricDiffs) != 7 {
		t.Errorf("expected 7 metric diffs, got %d", len(result.MetricDiffs))
	}

	if len(result.SourceDiffs) == 0 {
		t.Error("expected source diffs")
	}

	if len(result.RegressionTags) == 0 {
		t.Error("expected regression tags")
	}

	if result.OverallSummary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestCompareProfilesError(t *testing.T) {
	_, err := CompareProfiles(nil, &storage.ProfileRecord{}, "", "", nil)
	if err == nil {
		t.Error("expected error when old record is nil")
	}
}

func TestDiffHintGeneration(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		contains string
	}{
		{"loop with io", "for line in f.readlines():\n    process(line)", "IO"},
		{"memory alloc", "data = make([]int, 1000000)", "memory"},
		{"recursive", "func fib(n int) int { return fib(n-1) + fib(n-2) } // recursive", "recursive"},
		{"simple", "x = 1 + 2", "Normal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := generateDiffHint("", tt.line, "added")
			if !contains(hint, tt.contains) {
				t.Errorf("hint %q should contain %q", hint, tt.contains)
			}
		})
	}
}
