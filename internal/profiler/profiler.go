package profiler

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"profiler/internal/comparator"
	"profiler/internal/sandbox"
	"profiler/internal/storage"
	pb "profiler/proto"
)

type PerformanceProfiler struct {
	sandbox *sandbox.Sandbox
	storage *storage.Storage
}

func NewPerformanceProfiler(sb *sandbox.Sandbox, st *storage.Storage) *PerformanceProfiler {
	return &PerformanceProfiler{
		sandbox: sb,
		storage: st,
	}
}

func (p *PerformanceProfiler) ProfileCode(ctx context.Context, req *pb.ProfileRequest) (*pb.ProfileResponse, error) {
	language := req.Language.String()
	if language == "PYTHON" {
		language = "python"
	} else if language == "JAVA" {
		language = "java"
	} else if language == "GO" {
		language = "go"
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	execResult, err := p.sandbox.Execute(ctx, language, req.Code, timeout)
	if err != nil {
		return nil, fmt.Errorf("execution failed: %w", err)
	}

	profileStats, _, err := sandbox.ParseProfileOutput(execResult.Stdout, execResult.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse profile output: %w", err)
	}

	ioReadCount, ioWriteCount := sandbox.CountIOOperations(execResult.Stderr)
	if execResult.ResourceData != nil {
		if execResult.ResourceData.IOReadBytes > 0 {
			ioReadCount += execResult.ResourceData.IOReadBytes / 4096
		}
		if execResult.ResourceData.IOWriteBytes > 0 {
			ioWriteCount += execResult.ResourceData.IOWriteBytes / 4096
		}
	}

	memoryPeakMB := float64(profileStats.MemoryPeakBytes) / (1024 * 1024)
	if execResult.ResourceData != nil && execResult.ResourceData.MemoryPeak > memoryPeakMB {
		memoryPeakMB = execResult.ResourceData.MemoryPeak
	}

	cpuTimeMs := profileStats.CPUTimeMs
	if execResult.AdjustedCPUTime > 0 {
		cpuTimeMs = execResult.AdjustedCPUTime
	}

	metrics := &pb.PerformanceMetrics{
		CpuTimeMs:       cpuTimeMs,
		MemoryPeakMb:    memoryPeakMB,
		IoReadCount:     ioReadCount,
		IoWriteCount:    ioWriteCount,
		GcCount:         profileStats.GCCount,
		GcTimeMs:        profileStats.GCTimeMs,
		ExecutionTimeMs: float64(execResult.Duration.Milliseconds()),
		Success:         profileStats.Success,
		ErrorMessage:    profileStats.Error,
	}

	autoTags := sandbox.GenerateAutoTags(profileStats)
	if execResult.KilledByOOM {
		autoTags = append(autoTags, "oom_killed")
		metrics.Success = false
		if metrics.ErrorMessage == "" {
			metrics.ErrorMessage = "Out of memory"
		}
	}
	if execResult.TimedOut {
		autoTags = append(autoTags, "timeout")
		metrics.Success = false
		if metrics.ErrorMessage == "" {
			metrics.ErrorMessage = "Execution timed out"
		}
	}
	if execResult.CalibrationFactor != 0 && execResult.CalibrationFactor > 1.5 {
		autoTags = append(autoTags, "high_contention")
	}
	allTags := mergeTags(req.Tags, autoTags)

	profileID := uuid.New().String()

	record := &storage.ProfileRecord{
		ProfileID:       profileID,
		Language:        language,
		FunctionName:    req.FunctionName,
		CPUTimeMs:       metrics.CpuTimeMs,
		MemoryPeakMB:    metrics.MemoryPeakMb,
		IOReadCount:     metrics.IoReadCount,
		IOWriteCount:    metrics.IoWriteCount,
		GCCount:         metrics.GcCount,
		GCTimeMs:        metrics.GcTimeMs,
		ExecutionTimeMs: metrics.ExecutionTimeMs,
		Success:         metrics.Success,
		ErrorMessage:    metrics.ErrorMessage,
		Tags:            allTags,
		Timestamp:       time.Now(),
	}

	if err := p.storage.WriteProfile(ctx, record); err != nil {
		return nil, fmt.Errorf("failed to write profile: %w", err)
	}

	return &pb.ProfileResponse{
		ProfileId:    profileID,
		Metrics:      metrics,
		AssignedTags: allTags,
	}, nil
}

func (p *PerformanceProfiler) QueryProfiles(ctx context.Context, req *pb.ProfileQueryRequest) (*pb.ProfileQueryResponse, error) {
	filter := &storage.QueryFilter{
		Limit: int(req.Limit),
	}

	if req.TagQuery != nil {
		filter.Tags = req.TagQuery.Tags
		filter.TagOperator = req.TagQuery.Operator
	}

	if req.StartTimeUnix > 0 {
		filter.StartTime = time.Unix(req.StartTimeUnix, 0)
	}
	if req.EndTimeUnix > 0 {
		filter.EndTime = time.Unix(req.EndTimeUnix, 0)
	}

	records, totalCount, err := p.storage.QueryProfiles(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	entries := make([]*pb.ProfileEntry, 0, len(records))
	for _, record := range records {
		lang := pb.Language_PYTHON
		switch record.Language {
		case "java":
			lang = pb.Language_JAVA
		case "go":
			lang = pb.Language_GO
		}

		entry := &pb.ProfileEntry{
			ProfileId:    record.ProfileID,
			Language:     lang,
			FunctionName: record.FunctionName,
			Metrics: &pb.PerformanceMetrics{
				CpuTimeMs:       record.CPUTimeMs,
				MemoryPeakMb:    record.MemoryPeakMB,
				IoReadCount:     record.IOReadCount,
				IoWriteCount:    record.IOWriteCount,
				GcCount:         record.GCCount,
				GcTimeMs:        record.GCTimeMs,
				ExecutionTimeMs: record.ExecutionTimeMs,
				Success:         record.Success,
				ErrorMessage:    record.ErrorMessage,
			},
			Tags:          record.Tags,
			TimestampUnix: record.Timestamp.Unix(),
		}
		entries = append(entries, entry)
	}

	return &pb.ProfileQueryResponse{
		Profiles:   entries,
		TotalCount: int32(totalCount),
	}, nil
}

func mergeTags(userTags, autoTags []string) []string {
	tagSet := make(map[string]bool)
	for _, t := range userTags {
		tagSet[t] = true
	}
	for _, t := range autoTags {
		tagSet[t] = true
	}

	result := make([]string, 0, len(tagSet))
	for t := range tagSet {
		result = append(result, t)
	}
	return result
}

func (p *PerformanceProfiler) CompareProfiles(ctx context.Context, req *pb.CompareProfilesRequest) (*pb.CompareProfilesResponse, error) {
	if req.OldProfileId == "" || req.NewProfileId == "" {
		return nil, fmt.Errorf("both old_profile_id and new_profile_id are required")
	}

	oldRecord, err := p.storage.GetProfileByID(ctx, req.OldProfileId)
	if err != nil {
		return nil, fmt.Errorf("failed to get old profile: %w", err)
	}

	newRecord, err := p.storage.GetProfileByID(ctx, req.NewProfileId)
	if err != nil {
		return nil, fmt.Errorf("failed to get new profile: %w", err)
	}

	var cfg *comparator.CompareConfig
	if req.SignificanceThreshold > 0 {
		cfg = comparator.DefaultCompareConfig()
		cfg.SignificanceThreshold = req.SignificanceThreshold
	}

	result, err := comparator.CompareProfiles(
		oldRecord, newRecord,
		req.OldSourceCode, req.NewSourceCode,
		cfg,
	)
	if err != nil {
		return nil, fmt.Errorf("comparison failed: %w", err)
	}

	return &pb.CompareProfilesResponse{
		OldProfileId:    req.OldProfileId,
		NewProfileId:    req.NewProfileId,
		TimeDeltaSeconds: result.TimeDeltaSec,
		MetricDiffs:     result.MetricDiffs,
		OverallSummary:  result.OverallSummary,
		PerformanceTrend: result.PerformanceTrend,
		SourceDiffs:     result.SourceDiffs,
		RegressionTags:  result.RegressionTags,
		ImprovementTags: result.ImprovementTags,
	}, nil
}
