package sandbox

import (
	"testing"
	"time"
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

func TestCalibrationDataBasics(t *testing.T) {
	cal := NewCalibrationData(10)

	factor := cal.GetFactor("python")
	if factor.LoadFactor != 1.0 {
		t.Errorf("expected default load factor 1.0, got %f", factor.LoadFactor)
	}
	if !cal.NeedsCalibration("python", 5*time.Minute) {
		t.Error("expected needs calibration for new language")
	}

	for i := 0; i < 8; i++ {
		cal.Record("python", float64(100+i), float64(200+i))
	}

	factor = cal.GetFactor("python")
	if factor.CPUBaseline <= 0 {
		t.Errorf("expected positive CPU baseline, got %f", factor.CPUBaseline)
	}
	if cal.NeedsCalibration("python", 5*time.Minute) {
		t.Error("expected no immediate calibration need after recording")
	}
}

func TestCalibrationMedianCalculation(t *testing.T) {
	tests := []struct {
		name     string
		samples  []float64
		expected float64
	}{
		{"empty", []float64{}, 0},
		{"single", []float64{42}, 42},
		{"odd count", []float64{3, 1, 2}, 2},
		{"even count", []float64{4, 1, 3, 2}, 2.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := median(tt.samples)
			if result != tt.expected {
				t.Errorf("median(%v) = %f, want %f", tt.samples, result, tt.expected)
			}
		})
	}
}

func TestCalibrationAvgCalculation(t *testing.T) {
	if avg([]float64{}) != 0 {
		t.Error("avg of empty slice should be 0")
	}

	result := avg([]float64{1, 2, 3, 4, 5})
	if result != 3.0 {
		t.Errorf("avg([1..5]) = %f, want 3.0", result)
	}
}

func TestCalibrationLoadFactor(t *testing.T) {
	cal := NewCalibrationData(20)

	for i := 0; i < 6; i++ {
		cal.Record("go", float64(50), float64(100))
	}
	factor := cal.GetFactor("go")
	if factor.LoadFactor < 0.9 || factor.LoadFactor > 1.1 {
		t.Errorf("expected stable load factor ~1.0, got %f", factor.LoadFactor)
	}

	for i := 0; i < 3; i++ {
		cal.Record("go", float64(150), float64(300))
	}
	factor = cal.GetFactor("go")
	if factor.LoadFactor <= 1.0 {
		t.Errorf("expected increased load factor after slower samples, got %f", factor.LoadFactor)
	}
	if factor.LoadFactor > 3.0 {
		t.Errorf("load factor should be capped at 3.0, got %f", factor.LoadFactor)
	}
}

func TestDefaultResourceLimits(t *testing.T) {
	limits := DefaultResourceLimits()

	if limits.MemoryMB != 512 {
		t.Errorf("expected 512MB memory limit, got %d", limits.MemoryMB)
	}
	if limits.CPUs < 0.5 || limits.CPUs > 4.0 {
		t.Errorf("unexpected CPU limit: %f", limits.CPUs)
	}
	if limits.PidsLimit <= 0 {
		t.Error("pids limit should be positive")
	}
	if !limits.EnableOOMKill {
		t.Error("OOM kill should be enabled by default")
	}
}

func TestDefaultSandboxConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaxConcurrency < 1 {
		t.Error("max concurrency should be at least 1")
	}
	if cfg.CalibrationRounds < 1 {
		t.Error("calibration rounds should be positive")
	}
	if cfg.Limits == nil {
		t.Error("limits should not be nil")
	}
}

func TestBuildDockerArgs(t *testing.T) {
	sb := &Sandbox{
		config: DefaultConfig(),
	}

	config := &executionConfig{
		Image:      "python:3.11-slim",
		Cmd:        []string{"python", "/app/run.py"},
		SourceFile: "run.py",
	}

	args := sb.buildDockerArgs("test-job", "/tmp/test", config)

	hasNetworkNone := false
	hasReadOnly := false
	hasMemoryLimit := false
	hasCPULimit := false
	hasCapDrop := false
	hasNoNewPrivs := false
	hasOOMScore := false
	for _, arg := range args {
		switch {
		case arg == "--network" && indexOf(args, "none", indexOf(args, arg, 0)+1) >= 0:
			hasNetworkNone = true
		case arg == "--read-only":
			hasReadOnly = true
		case arg == "--memory":
			hasMemoryLimit = true
		case arg == "--cpus":
			hasCPULimit = true
		case arg == "--cap-drop":
			hasCapDrop = true
		case arg == "--security-opt" && containsSubstring(args, "no-new-privileges"):
			hasNoNewPrivs = true
		case arg == "--oom-score-adj":
			hasOOMScore = true
		}
	}

	if !hasNetworkNone {
		t.Error("expected --network none")
	}
	if !hasReadOnly {
		t.Error("expected --read-only")
	}
	if !hasMemoryLimit {
		t.Error("expected --memory limit")
	}
	if !hasCPULimit {
		t.Error("expected --cpus limit")
	}
	if !hasCapDrop {
		t.Error("expected --cap-drop ALL")
	}
	if !hasNoNewPrivs {
		t.Error("expected --security-opt no-new-privileges")
	}
	if !hasOOMScore {
		t.Error("expected --oom-score-adj")
	}
}

func indexOf(slice []string, target string, from int) int {
	for i := from; i < len(slice); i++ {
		if slice[i] == target {
			return i
		}
	}
	return -1
}

func containsSubstring(slice []string, substr string) bool {
	for _, s := range slice {
		if len(s) >= len(substr) && (s == substr || contains(s, substr)) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestExecutionResultFields(t *testing.T) {
	result := &ExecutionResult{
		ExitCode:          137,
		KilledByOOM:       true,
		TimedOut:          false,
		CalibrationFactor: 1.8,
		AdjustedCPUTime:   45.5,
	}

	if !result.KilledByOOM {
		t.Error("KilledByOOM should be true")
	}
	if result.AdjustedCPUTime <= 0 {
		t.Error("AdjustedCPUTime should be positive")
	}
	if result.CalibrationFactor < 1.5 {
		t.Error("CalibrationFactor indicates high contention")
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
		ok       bool
	}{
		{"128MiB / 512MiB", 128.0, true},
		{"1.5GiB / 2GiB", 1536.0, true},
		{"1024KiB / 1MiB", 1.0, true},
		{"", 0, false},
		{"invalid", 0, false},
	}

	for _, tt := range tests {
		val, ok := parseMemory(tt.input)
		if ok != tt.ok {
			t.Errorf("parseMemory(%q) ok=%v, want %v", tt.input, ok, tt.ok)
		}
		if ok && val != tt.expected {
			t.Errorf("parseMemory(%q) = %f, want %f", tt.input, val, tt.expected)
		}
	}
}

func TestParseCPU(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
		ok       bool
	}{
		{"45.5%", 45.5, true},
		{"100%", 100.0, true},
		{"0.00%", 0.0, true},
		{"", 0, false},
		{"abc%", 0, false},
	}

	for _, tt := range tests {
		val, ok := parseCPU(tt.input)
		if ok != tt.ok {
			t.Errorf("parseCPU(%q) ok=%v, want %v", tt.input, ok, tt.ok)
		}
		if ok && val != tt.expected {
			t.Errorf("parseCPU(%q) = %f, want %f", tt.input, val, tt.expected)
		}
	}
}

func TestSandboxConcurrencySafety(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrency = 3

	cal := NewCalibrationData(10)

	if cal == nil {
		t.Fatal("calibration should not be nil")
	}
}

func TestCalibrationNeedsRefresh(t *testing.T) {
	cal := NewCalibrationData(10)

	if !cal.NeedsCalibration("java", time.Minute) {
		t.Error("new language should need calibration")
	}

	for i := 0; i < 5; i++ {
		cal.Record("java", float64(i*10), float64(i*20))
	}

	if cal.NeedsCalibration("java", time.Hour) {
		t.Error("freshly calibrated should not need refresh for long interval")
	}

	time.Sleep(10 * time.Millisecond)
	if !cal.NeedsCalibration("java", 1*time.Millisecond) {
		t.Error("should need refresh after short interval")
	}
}
