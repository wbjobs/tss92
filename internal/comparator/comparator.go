package comparator

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"profiler/internal/storage"
	pb "profiler/proto"
)

type CompareConfig struct {
	SignificanceThreshold float64
	CPUWeight             float64
	MemoryWeight          float64
	IOWeight              float64
	GCWeight              float64
}

func DefaultCompareConfig() *CompareConfig {
	return &CompareConfig{
		SignificanceThreshold: 10.0,
		CPUWeight:             0.4,
		MemoryWeight:          0.25,
		IOWeight:              0.2,
		GCWeight:              0.15,
	}
}

type ComparisonResult struct {
	MetricDiffs     []*pb.MetricDiff
	SourceDiffs     []*pb.SourceLineDiff
	OverallSummary  string
	PerformanceTrend string
	RegressionTags  []string
	ImprovementTags []string
	TimeDeltaSec    int64
}

func CompareProfiles(
	oldRecord, newRecord *storage.ProfileRecord,
	oldSource, newSource string,
	cfg *CompareConfig,
) (*ComparisonResult, error) {
	if oldRecord == nil || newRecord == nil {
		return nil, fmt.Errorf("both profile records are required")
	}

	if cfg == nil {
		cfg = DefaultCompareConfig()
	}

	metricDiffs := calculateMetricDiffs(oldRecord, newRecord, cfg.SignificanceThreshold)

	timeDelta := int64(newRecord.Timestamp.Sub(oldRecord.Timestamp).Seconds())

	var sourceDiffs []*pb.SourceLineDiff
	if oldSource != "" && newSource != "" {
		sourceDiffs = calculateSourceDiffs(oldSource, newSource, metricDiffs)
	}

	trend, regressionTags, improvementTags := analyzeTrend(metricDiffs, cfg)
	summary := generateSummary(oldRecord, newRecord, metricDiffs, trend)

	return &ComparisonResult{
		MetricDiffs:      metricDiffs,
		SourceDiffs:      sourceDiffs,
		OverallSummary:   summary,
		PerformanceTrend: trend,
		RegressionTags:   regressionTags,
		ImprovementTags:  improvementTags,
		TimeDeltaSec:     timeDelta,
	}, nil
}

func calculateMetricDiffs(
	oldRecord, newRecord *storage.ProfileRecord,
	significanceThreshold float64,
) []*pb.MetricDiff {
	metrics := []struct {
		name     string
		oldVal   float64
		newVal   float64
		higherIsWorse bool
	}{
		{"cpu_time_ms", oldRecord.CPUTimeMs, newRecord.CPUTimeMs, true},
		{"memory_peak_mb", oldRecord.MemoryPeakMB, newRecord.MemoryPeakMB, true},
		{"io_read_count", float64(oldRecord.IOReadCount), float64(newRecord.IOReadCount), true},
		{"io_write_count", float64(oldRecord.IOWriteCount), float64(newRecord.IOWriteCount), true},
		{"gc_count", float64(oldRecord.GCCount), float64(newRecord.GCCount), true},
		{"gc_time_ms", oldRecord.GCTimeMs, newRecord.GCTimeMs, true},
		{"execution_time_ms", oldRecord.ExecutionTimeMs, newRecord.ExecutionTimeMs, true},
	}

	var diffs []*pb.MetricDiff

	for _, m := range metrics {
		absDiff := m.newVal - m.oldVal

		var pctDiff float64
		if m.oldVal != 0 {
			pctDiff = (absDiff / math.Abs(m.oldVal)) * 100
		} else if m.newVal > 0 {
			pctDiff = 100
		} else {
			pctDiff = 0
		}

		changeType := "unchanged"
		if absDiff > 0 {
			if m.higherIsWorse {
				changeType = "regression"
			} else {
				changeType = "improvement"
			}
		} else if absDiff < 0 {
			if m.higherIsWorse {
				changeType = "improvement"
			} else {
				changeType = "regression"
			}
		}

		isSignificant := math.Abs(pctDiff) >= significanceThreshold ||
			(m.oldVal < 1 && m.newVal >= 1) ||
			(math.Abs(absDiff) > 50 && m.name == "cpu_time_ms")

		diffs = append(diffs, &pb.MetricDiff{
			MetricName:     m.name,
			OldValue:       m.oldVal,
			NewValue:       m.newVal,
			AbsoluteDiff:   absDiff,
			PercentageDiff: pctDiff,
			ChangeType:     changeType,
			IsSignificant:  isSignificant,
		})
	}

	return diffs
}

type lineChange struct {
	lineNum int
	oldLine string
	newLine string
	lineType string
}

func calculateSourceDiffs(oldSource, newSource string, metricDiffs []*pb.MetricDiff) []*pb.SourceLineDiff {
	oldLines := strings.Split(oldSource, "\n")
	newLines := strings.Split(newSource, "\n")

	changes := lcsDiff(oldLines, newLines)

	cpuRegression := 0.0
	memoryRegression := 0.0
	for _, d := range metricDiffs {
		if d.ChangeType == "regression" && d.IsSignificant {
			switch d.MetricName {
			case "cpu_time_ms":
				cpuRegression = math.Abs(d.PercentageDiff)
			case "memory_peak_mb":
				memoryRegression = math.Abs(d.PercentageDiff)
			}
		}
	}

	var result []*pb.SourceLineDiff
	for _, ch := range changes {
		impact := calculatePerformanceImpact(ch.oldLine, ch.newLine, ch.lineType, cpuRegression, memoryRegression)
		hint := generateDiffHint(ch.oldLine, ch.newLine, ch.lineType)

		result = append(result, &pb.SourceLineDiff{
			LineNumber:           int32(ch.lineNum),
			OldLine:              ch.oldLine,
			NewLine:              ch.newLine,
			LineType:             ch.lineType,
			DiffHint:             hint,
			PerformanceImpactScore: impact,
		})
	}

	return result
}

func lcsDiff(oldLines, newLines []string) []lineChange {
	m, n := len(oldLines), len(newLines)

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if normalizeLine(oldLines[i]) == normalizeLine(newLines[j]) {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}

	var changes []lineChange
	i, j := 0, 0
	lineNum := 1

	for i < m || j < n {
		switch {
		case i < m && j < n && normalizeLine(oldLines[i]) == normalizeLine(newLines[j]):
			i++
			j++
			lineNum++
		case j < n && (i == m || dp[i+1][j] <= dp[i][j+1]):
			changes = append(changes, lineChange{
				lineNum:  lineNum,
				oldLine:  "",
				newLine:  newLines[j],
				lineType: "added",
			})
			j++
			lineNum++
		default:
			changes = append(changes, lineChange{
				lineNum:  lineNum,
				oldLine:  oldLines[i],
				newLine:  "",
				lineType: "removed",
			})
			i++
			lineNum++
		}
	}

	return changes
}

func normalizeLine(s string) string {
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var (
	loopPattern      = regexp.MustCompile(`\b(for|while|foreach|range)\b`)
	allocPattern     = regexp.MustCompile(`\b(new|make|malloc|alloc|ArrayList|HashMap|list\.append|append\()`)
	ioPattern        = regexp.MustCompile(`\b(read|write|fread|fwrite|fopen|open|print|log|println|fmt\.Print|ioutil\.Read|os\.Read|io\.Read)`)
	complexPattern   = regexp.MustCompile(`\b(append|delete|copy|sort|reverse|map\[|append\(|\[\].*append|regexp|\.compile|\.Match)`)
	funcCallPattern  = regexp.MustCompile(`\w+\(`)
	recursivePattern = regexp.MustCompile(`profileTarget|recursive|recurse`)
)

func calculatePerformanceImpact(oldLine, newLine, lineType string, cpuReg, memReg float64) float64 {
	impact := 0.0

	targetLine := newLine
	if lineType == "removed" {
		targetLine = oldLine
	}

	targetLower := strings.ToLower(targetLine)

	if loopPattern.MatchString(targetLower) {
		impact += 30
	}
	if allocPattern.MatchString(targetLower) {
		impact += 25
	}
	if ioPattern.MatchString(targetLower) {
		impact += 35
	}
	if complexPattern.MatchString(targetLower) {
		impact += 20
	}
	if recursivePattern.MatchString(targetLower) {
		impact += 50
	}

	callCount := len(funcCallPattern.FindAllString(targetLine, -1))
	impact += float64(callCount) * 8

	nestedDepth := strings.Count(targetLine, "{") + strings.Count(targetLine, "(")
	impact += float64(nestedDepth) * 5

	baseImpact := 10.0
	if cpuReg > 0 {
		impact *= (1 + cpuReg/100)
	}
	if memReg > 0 {
		impact *= (1 + memReg/200)
	}
	impact += baseImpact

	if lineType == "removed" {
		impact = -impact
	}

	return math.Round(impact*100) / 100
}

func generateDiffHint(oldLine, newLine, lineType string) string {
	switch lineType {
	case "added":
		return analyzeLine(newLine)
	case "removed":
		return analyzeLine(oldLine)
	default:
		return analyzeLine(newLine)
	}
}

func analyzeLine(line string) string {
	lower := strings.ToLower(line)

	switch {
	case loopPattern.MatchString(lower) && allocPattern.MatchString(lower):
		return "Memory allocation inside loop may increase GC pressure"
	case loopPattern.MatchString(lower) && ioPattern.MatchString(lower):
		return "IO operation inside loop may significantly degrade performance"
	case loopPattern.MatchString(lower):
		return "Loop structure change may affect CPU usage"
	case allocPattern.MatchString(lower):
		return "New memory allocation may affect memory usage and GC frequency"
	case ioPattern.MatchString(lower):
		return "IO operation change may affect IO latency"
	case recursivePattern.MatchString(lower):
		return "Recursive call may cause stack overflow or performance degradation"
	case complexPattern.MatchString(lower):
		return "Complex operation (regex/sort/hash) may increase CPU overhead"
	case funcCallPattern.MatchString(line):
		return "Function call change, need to check performance of callee"
	default:
		return "Normal code change"
	}
}

func analyzeTrend(diffs []*pb.MetricDiff, cfg *CompareConfig) (string, []string, []string) {
	var regressions, improvements []string
	regressionScore := 0.0
	improvementScore := 0.0

	for _, d := range diffs {
		if !d.IsSignificant {
			continue
		}

		switch d.MetricName {
		case "cpu_time_ms":
			if d.ChangeType == "regression" {
				regressions = append(regressions, "cpu_regression")
				regressionScore += math.Abs(d.PercentageDiff) * cfg.CPUWeight
			} else {
				improvements = append(improvements, "cpu_improved")
				improvementScore += math.Abs(d.PercentageDiff) * cfg.CPUWeight
			}
		case "memory_peak_mb":
			if d.ChangeType == "regression" {
				regressions = append(regressions, "memory_regression")
				regressionScore += math.Abs(d.PercentageDiff) * cfg.MemoryWeight
			} else {
				improvements = append(improvements, "memory_improved")
				improvementScore += math.Abs(d.PercentageDiff) * cfg.MemoryWeight
			}
		case "io_read_count", "io_write_count":
			if d.ChangeType == "regression" {
				regressions = append(regressions, "io_regression")
				regressionScore += math.Abs(d.PercentageDiff) * cfg.IOWeight
			} else {
				improvements = append(improvements, "io_improved")
				improvementScore += math.Abs(d.PercentageDiff) * cfg.IOWeight
			}
		case "gc_count", "gc_time_ms":
			if d.ChangeType == "regression" {
				regressions = append(regressions, "gc_regression")
				regressionScore += math.Abs(d.PercentageDiff) * cfg.GCWeight
			} else {
				improvements = append(improvements, "gc_improved")
				improvementScore += math.Abs(d.PercentageDiff) * cfg.GCWeight
			}
		}
	}

	trend := "stable"
	netScore := improvementScore - regressionScore
	threshold := cfg.SignificanceThreshold * 0.3

	switch {
	case netScore > threshold:
		trend = "improved"
	case netScore < -threshold:
		trend = "regressed"
	}

	if regressionScore >= cfg.SignificanceThreshold*2 {
		regressions = append(regressions, "severe_regression")
	}
	if improvementScore >= cfg.SignificanceThreshold*2 {
		improvements = append(improvements, "significant_improvement")
	}

	return trend, uniqueStrings(regressions), uniqueStrings(improvements)
}

func uniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

func generateSummary(
	oldRecord, newRecord *storage.ProfileRecord,
	diffs []*pb.MetricDiff,
	trend string,
) string {
	var significantDiffs []*pb.MetricDiff
	for _, d := range diffs {
		if d.IsSignificant {
			significantDiffs = append(significantDiffs, d)
		}
	}

	if len(significantDiffs) == 0 {
		return fmt.Sprintf(
			"Performance stable, all metric changes below threshold. CPU: %.1fms → %.1fms, Memory: %.1fMB → %.1fMB",
			oldRecord.CPUTimeMs, newRecord.CPUTimeMs,
			oldRecord.MemoryPeakMB, newRecord.MemoryPeakMB,
		)
	}

	var parts []string
	for _, d := range significantDiffs {
		if d.ChangeType == "regression" {
			parts = append(parts, fmt.Sprintf(
				"%s上升%.1f%% (%.1f → %.1f)",
				d.MetricName, d.PercentageDiff, d.OldValue, d.NewValue,
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"%s下降%.1f%% (%.1f → %.1f)",
				d.MetricName, math.Abs(d.PercentageDiff), d.OldValue, d.NewValue,
			))
		}
	}

	trendDesc := map[string]string{
		"improved":  "Overall performance improved",
		"regressed": "Overall performance regressed",
		"stable":    "Performance roughly the same",
	}[trend]

	return fmt.Sprintf(
		"%s. Key changes: %s",
		trendDesc, strings.Join(parts, "; "),
	)
}
