package storage

import (
	"context"
	"fmt"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

type ProfileRecord struct {
	ProfileID        string
	Language         string
	FunctionName     string
	CPUTimeMs        float64
	MemoryPeakMB     float64
	IOReadCount      int64
	IOWriteCount     int64
	GCCount          int64
	GCTimeMs         float64
	ExecutionTimeMs  float64
	Success          bool
	ErrorMessage     string
	Tags             []string
	Timestamp        time.Time
}

type Storage struct {
	client   influxdb2.Client
	writeAPI api.WriteAPI
	queryAPI api.QueryAPI
	org      string
	bucket   string
}

func NewStorage(url, token, org, bucket string) *Storage {
	client := influxdb2.NewClient(url, token)
	return &Storage{
		client:   client,
		writeAPI: client.WriteAPI(org, bucket),
		queryAPI: client.QueryAPI(org),
		org:      org,
		bucket:   bucket,
	}
}

func (s *Storage) Close() {
	s.writeAPI.Flush()
	s.client.Close()
}

func (s *Storage) WriteProfile(ctx context.Context, record *ProfileRecord) error {
	tags := make(map[string]string)
	tags["language"] = record.Language
	tags["function_name"] = record.FunctionName
	tags["profile_id"] = record.ProfileID
	tags["success"] = fmt.Sprintf("%t", record.Success)

	for _, tag := range record.Tags {
		tags[fmt.Sprintf("tag_%s", tag)] = "true"
	}

	fields := map[string]interface{}{
		"cpu_time_ms":       record.CPUTimeMs,
		"memory_peak_mb":    record.MemoryPeakMB,
		"io_read_count":     record.IOReadCount,
		"io_write_count":    record.IOWriteCount,
		"gc_count":          record.GCCount,
		"gc_time_ms":        record.GCTimeMs,
		"execution_time_ms": record.ExecutionTimeMs,
		"error_message":     record.ErrorMessage,
	}

	tagsStr := make([]string, len(record.Tags))
	copy(tagsStr, record.Tags)
	fields["tags"] = tagsStr

	point := write.NewPoint("performance_profiles", tags, fields, record.Timestamp)
	s.writeAPI.WritePoint(point)

	return nil
}

type QueryFilter struct {
	Tags          []string
	TagOperator   string
	StartTime     time.Time
	EndTime       time.Time
	Limit         int
	Language      string
	FunctionName  string
	SuccessOnly   bool
}

func (s *Storage) QueryProfiles(ctx context.Context, filter *QueryFilter) ([]*ProfileRecord, int, error) {
	rangeStart := "-30d"
	if !filter.StartTime.IsZero() {
		rangeStart = filter.StartTime.Format(time.RFC3339)
	}
	rangeEnd := "now()"
	if !filter.EndTime.IsZero() {
		rangeEnd = filter.EndTime.Format(time.RFC3339)
	}

	fluxQuery := fmt.Sprintf(`
		from(bucket: "%s")
			|> range(start: %s, stop: %s)
			|> filter(fn: (r) => r._measurement == "performance_profiles")
	`, s.bucket, rangeStart, rangeEnd)

	if filter.Language != "" {
		fluxQuery += fmt.Sprintf(`|> filter(fn: (r) => r.language == "%s")`, filter.Language)
	}

	if filter.FunctionName != "" {
		fluxQuery += fmt.Sprintf(`|> filter(fn: (r) => r.function_name == "%s")`, filter.FunctionName)
	}

	if filter.SuccessOnly {
		fluxQuery += `|> filter(fn: (r) => r.success == "true")`
	}

	if len(filter.Tags) > 0 {
		var tagConditions []string
		for _, tag := range filter.Tags {
			tagConditions = append(tagConditions, fmt.Sprintf(`r["tag_%s"] == "true"`, tag))
		}
		operator := "and"
		if filter.TagOperator == "or" {
			operator = "or"
		}
		if len(tagConditions) > 0 {
			fluxQuery += fmt.Sprintf(`|> filter(fn: (r) => %s)`, joinConditions(tagConditions, operator))
		}
	}

	fluxQuery += `
		|> pivot(rowKey: ["_time", "profile_id"], columnKey: ["_field"], valueColumn: "_value")
		|> keep(columns: ["_time", "profile_id", "language", "function_name", "success", "cpu_time_ms", "memory_peak_mb", "io_read_count", "io_write_count", "gc_count", "gc_time_ms", "execution_time_ms", "error_message", "tags"])
		|> sort(columns: ["_time"], desc: true)
	`

	if filter.Limit > 0 {
		fluxQuery += fmt.Sprintf(`|> limit(n: %d)`, filter.Limit)
	}

	result, err := s.queryAPI.Query(ctx, fluxQuery)
	if err != nil {
		return nil, 0, fmt.Errorf("query failed: %w", err)
	}
	defer result.Close()

	var records []*ProfileRecord
	for result.Next() {
		record := &ProfileRecord{
			Timestamp: result.Record().Time(),
		}

		values := result.Record().Values()
		if v, ok := values["profile_id"].(string); ok {
			record.ProfileID = v
		}
		if v, ok := values["language"].(string); ok {
			record.Language = v
		}
		if v, ok := values["function_name"].(string); ok {
			record.FunctionName = v
		}
		if v, ok := values["success"].(string); ok {
			record.Success = v == "true"
		}
		if v, ok := values["cpu_time_ms"].(float64); ok {
			record.CPUTimeMs = v
		}
		if v, ok := values["memory_peak_mb"].(float64); ok {
			record.MemoryPeakMB = v
		}
		if v, ok := values["io_read_count"].(int64); ok {
			record.IOReadCount = v
		} else if v, ok := values["io_read_count"].(float64); ok {
			record.IOReadCount = int64(v)
		}
		if v, ok := values["io_write_count"].(int64); ok {
			record.IOWriteCount = v
		} else if v, ok := values["io_write_count"].(float64); ok {
			record.IOWriteCount = int64(v)
		}
		if v, ok := values["gc_count"].(int64); ok {
			record.GCCount = v
		} else if v, ok := values["gc_count"].(float64); ok {
			record.GCCount = int64(v)
		}
		if v, ok := values["gc_time_ms"].(float64); ok {
			record.GCTimeMs = v
		}
		if v, ok := values["execution_time_ms"].(float64); ok {
			record.ExecutionTimeMs = v
		}
		if v, ok := values["error_message"].(string); ok {
			record.ErrorMessage = v
		}
		if v, ok := values["tags"].([]string); ok {
			record.Tags = v
		}

		records = append(records, record)
	}

	if result.Err() != nil {
		return nil, 0, result.Err()
	}

	countQuery := fmt.Sprintf(`
		from(bucket: "%s")
			|> range(start: %s, stop: %s)
			|> filter(fn: (r) => r._measurement == "performance_profiles" and r._field == "cpu_time_ms")
			|> count()
	`, s.bucket, rangeStart, rangeEnd)

	countResult, err := s.queryAPI.Query(ctx, countQuery)
	if err != nil {
		return records, len(records), nil
	}
	defer countResult.Close()

	totalCount := len(records)
	for countResult.Next() {
		if v, ok := countResult.Record().Value().(int64); ok {
			totalCount = int(v)
		}
	}

	return records, totalCount, nil
}

func joinConditions(conditions []string, operator string) string {
	if len(conditions) == 0 {
		return ""
	}
	result := conditions[0]
	for i := 1; i < len(conditions); i++ {
		result += fmt.Sprintf(" %s %s", operator, conditions[i])
	}
	return result
}

func (s *Storage) GetProfileByID(ctx context.Context, profileID string) (*ProfileRecord, error) {
	fluxQuery := fmt.Sprintf(`
		from(bucket: "%s")
			|> range(start: -90d)
			|> filter(fn: (r) => r._measurement == "performance_profiles")
			|> filter(fn: (r) => r.profile_id == "%s")
			|> pivot(rowKey: ["_time"], columnKey: ["_field"], valueColumn: "_value")
			|> limit(n: 1)
	`, s.bucket, profileID)

	result, err := s.queryAPI.Query(ctx, fluxQuery)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer result.Close()

	if result.Next() {
		record := &ProfileRecord{
			Timestamp: result.Record().Time(),
		}

		values := result.Record().Values()
		if v, ok := values["profile_id"].(string); ok {
			record.ProfileID = v
		}
		if v, ok := values["language"].(string); ok {
			record.Language = v
		}
		if v, ok := values["function_name"].(string); ok {
			record.FunctionName = v
		}
		if v, ok := values["success"].(string); ok {
			record.Success = v == "true"
		}
		if v, ok := values["cpu_time_ms"].(float64); ok {
			record.CPUTimeMs = v
		}
		if v, ok := values["memory_peak_mb"].(float64); ok {
			record.MemoryPeakMB = v
		}
		if v, ok := values["io_read_count"].(int64); ok {
			record.IOReadCount = v
		} else if v, ok := values["io_read_count"].(float64); ok {
			record.IOReadCount = int64(v)
		}
		if v, ok := values["io_write_count"].(int64); ok {
			record.IOWriteCount = v
		} else if v, ok := values["io_write_count"].(float64); ok {
			record.IOWriteCount = int64(v)
		}
		if v, ok := values["gc_count"].(int64); ok {
			record.GCCount = v
		} else if v, ok := values["gc_count"].(float64); ok {
			record.GCCount = int64(v)
		}
		if v, ok := values["gc_time_ms"].(float64); ok {
			record.GCTimeMs = v
		}
		if v, ok := values["execution_time_ms"].(float64); ok {
			record.ExecutionTimeMs = v
		}
		if v, ok := values["error_message"].(string); ok {
			record.ErrorMessage = v
		}
		if v, ok := values["tags"].([]string); ok {
			record.Tags = v
		}

		return record, nil
	}

	return nil, fmt.Errorf("profile not found: %s", profileID)
}
