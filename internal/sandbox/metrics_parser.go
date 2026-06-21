package sandbox

import (
	"encoding/json"
	"regexp"
	"strings"
)

type ContainerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage        uint64   `json:"total_usage"`
			UsageInKernelmode uint64   `json:"usage_in_kernelmode"`
			UsageInUsermode   uint64   `json:"usage_in_usermode"`
			PercpuUsage       []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
		ThrottlingData struct {
			Periods          uint64 `json:"periods"`
			ThrottledPeriods uint64 `json:"throttled_periods"`
			ThrottledTime    uint64 `json:"throttled_time"`
		} `json:"throttling_data"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage    uint64 `json:"usage"`
		MaxUsage uint64 `json:"max_usage"`
		Stats    struct {
			Rss uint64 `json:"rss"`
			Cache uint64 `json:"cache"`
		} `json:"stats"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
	BlkioStats struct {
		IoServiceBytesRecursive []struct {
			Major uint64 `json:"major"`
			Minor uint64 `json:"minor"`
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

func extractMemoryUsage(data []byte) (float64, bool) {
	var stats ContainerStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return 0, false
	}
	if stats.MemoryStats.Usage > 0 {
		return float64(stats.MemoryStats.Usage) / (1024 * 1024), true
	}
	return 0, false
}

func extractCPUUsage(data []byte) (float64, bool) {
	var stats ContainerStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return 0, false
	}

	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage)
	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)

	if systemDelta > 0 && cpuDelta > 0 {
		cpuPercent := (cpuDelta / systemDelta) * onlineCPUs * 100.0
		return cpuPercent, true
	}
	return 0, false
}

func extractIOUsage(data []byte) (int64, int64, bool) {
	var stats ContainerStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return 0, 0, false
	}

	var readBytes, writeBytes uint64
	for _, ioEntry := range stats.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(ioEntry.Op) {
		case "read":
			readBytes += ioEntry.Value
		case "write":
			writeBytes += ioEntry.Value
		}
	}
	return int64(readBytes), int64(writeBytes), true
}

type ProfileStats struct {
	CPUTimeMs       float64 `json:"cpu_time_ms"`
	WallTimeMs      float64 `json:"wall_time_ms"`
	MemoryPeakBytes int64   `json:"memory_peak_bytes"`
	GCCount         int64   `json:"gc_count"`
	GCTimeMs        float64 `json:"gc_time_ms"`
	Success         bool    `json:"success"`
	Error           string  `json:"error"`
}

var (
	statsPrefix  = "PROFILE_STATS:"
	resultPrefix = "PROFILE_RESULT:"
	errorPrefix  = "PROFILE_ERROR:"
)

func ParseProfileOutput(stdout, stderr string) (*ProfileStats, string, error) {
	var stats ProfileStats
	var result string

	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, statsPrefix) {
			jsonStr := strings.TrimPrefix(line, statsPrefix)
			jsonStr = strings.TrimSpace(jsonStr)
			if err := json.Unmarshal([]byte(jsonStr), &stats); err != nil {
				return nil, "", err
			}
		} else if strings.HasPrefix(line, resultPrefix) {
			result = strings.TrimSpace(strings.TrimPrefix(line, resultPrefix))
		}
	}

	if !stats.Success && stats.Error == "" {
		stats.Error = extractError(stderr)
	}

	return &stats, result, nil
}

func extractError(stderr string) string {
	errLines := strings.Split(stderr, "\n")
	for _, line := range errLines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, errorPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, errorPrefix))
		}
	}
	return strings.TrimSpace(stderr)
}

var (
	highCPUThreshold      = 500.0
	highMemoryThreshold   = 100.0
	highIOThreshold       = int64(1000)
	highGCThreshold       = int64(5)
)

func GenerateAutoTags(stats *ProfileStats) []string {
	var tags []string

	if !stats.Success {
		tags = append(tags, "execution_failed")
		return tags
	}

	if stats.CPUTimeMs > highCPUThreshold {
		tags = append(tags, "high_cpu")
	}
	if float64(stats.MemoryPeakBytes)/(1024*1024) > highMemoryThreshold {
		tags = append(tags, "memory_sensitive")
	}
	if stats.GCCount > highGCThreshold {
		tags = append(tags, "high_gc")
	}
	if len(tags) == 0 {
		tags = append(tags, "lightweight")
	}

	return tags
}

func CountIOOperations(stderr string) (int64, int64) {
	readRe := regexp.MustCompile(`(?i)\b(?:fread|pread|recv|read)\b`)
	writeRe := regexp.MustCompile(`(?i)\b(?:fwrite|pwrite|send|write)\b`)

	readCount := int64(len(readRe.FindAllString(stderr, -1)))
	writeCount := int64(len(writeRe.FindAllString(stderr, -1)))

	return readCount, writeCount
}
