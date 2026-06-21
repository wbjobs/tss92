package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "profiler/proto"
)

var (
	addr     = flag.String("addr", "localhost:50051", "gRPC server address")
	language = flag.String("lang", "python", "Language: python, java, or go")
	codeFile = flag.String("code", "", "Path to code file")
	function = flag.String("func", "profile_target", "Function name to profile")
	timeout  = flag.Int("timeout", 30, "Timeout in seconds")
	tags     = flag.String("tags", "", "Comma-separated tags")
	mode     = flag.String("mode", "profile", "Mode: profile, query, or compare")
	queryTags = flag.String("query-tags", "", "Tags to query (comma-separated)")
	limit    = flag.Int("limit", 10, "Query limit")
	oldProfile = flag.String("old-profile", "", "Old profile ID for comparison")
	newProfile = flag.String("new-profile", "", "New profile ID for comparison")
	oldCode   = flag.String("old-code", "", "Path to old source code file")
	newCode   = flag.String("new-code", "", "Path to new source code file")
	significance = flag.Float64("significance", 10.0, "Significance threshold percentage")
)

func main() {
	flag.Parse()

	conn, err := grpc.Dial(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewPerformanceProfilerClient(conn)
	ctx := context.Background()

	switch *mode {
	case "query":
		queryProfiles(ctx, client)
	case "compare":
		compareProfiles(ctx, client)
	default:
		profileCode(ctx, client)
	}
}

func profileCode(ctx context.Context, client pb.PerformanceProfilerClient) {
	code, err := readCodeFile(*codeFile)
	if err != nil {
		log.Fatalf("Failed to read code file: %v", err)
	}

	lang := pb.Language_PYTHON
	switch *language {
	case "java":
		lang = pb.Language_JAVA
	case "go":
		lang = pb.Language_GO
	}

	var tagList []string
	if *tags != "" {
		for _, t := range splitTags(*tags) {
			tagList = append(tagList, t)
		}
	}

	req := &pb.ProfileRequest{
		Language:       lang,
		Code:           code,
		FunctionName:   *function,
		Tags:           tagList,
		TimeoutSeconds: int32(*timeout),
	}

	log.Printf("Profiling %s code...", *language)
	start := time.Now()

	resp, err := client.ProfileCode(ctx, req)
	if err != nil {
		log.Fatalf("Profile failed: %v", err)
	}

	elapsed := time.Since(start)
	log.Printf("Profile completed in %v", elapsed)

	fmt.Println("\n=== Performance Profile ===")
	fmt.Printf("Profile ID:     %s\n", resp.ProfileId)
	fmt.Printf("Success:        %t\n", resp.Metrics.Success)
	if !resp.Metrics.Success {
		fmt.Printf("Error:          %s\n", resp.Metrics.ErrorMessage)
	}
	fmt.Printf("CPU Time:       %.2f ms\n", resp.Metrics.CpuTimeMs)
	fmt.Printf("Memory Peak:    %.2f MB\n", resp.Metrics.MemoryPeakMb)
	fmt.Printf("IO Reads:       %d\n", resp.Metrics.IoReadCount)
	fmt.Printf("IO Writes:      %d\n", resp.Metrics.IoWriteCount)
	fmt.Printf("GC Count:       %d\n", resp.Metrics.GcCount)
	fmt.Printf("GC Time:        %.2f ms\n", resp.Metrics.GcTimeMs)
	fmt.Printf("Execution Time: %.2f ms\n", resp.Metrics.ExecutionTimeMs)

	if len(resp.AssignedTags) > 0 {
		fmt.Println("\nTags:")
		for _, tag := range resp.AssignedTags {
			fmt.Printf("  - %s\n", tag)
		}
	}
}

func queryProfiles(ctx context.Context, client pb.PerformanceProfilerClient) {
	var tagList []string
	if *queryTags != "" {
		for _, t := range splitTags(*queryTags) {
			tagList = append(tagList, t)
		}
	}

	req := &pb.ProfileQueryRequest{
		TagQuery: &pb.TagQuery{
			Tags:     tagList,
			Operator: "and",
		},
		Limit: int32(*limit),
	}

	log.Printf("Querying profiles with tags: %v", tagList)

	resp, err := client.QueryProfiles(ctx, req)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}

	fmt.Printf("\n=== Query Results (total: %d) ===\n", resp.TotalCount)
	for i, entry := range resp.Profiles {
		fmt.Printf("\n--- Profile %d ---\n", i+1)
		fmt.Printf("ID:           %s\n", entry.ProfileId)
		fmt.Printf("Language:     %s\n", entry.Language.String())
		fmt.Printf("Function:     %s\n", entry.FunctionName)
		fmt.Printf("Time:         %s\n", time.Unix(entry.TimestampUnix, 0).Format(time.RFC3339))
		fmt.Printf("CPU Time:     %.2f ms\n", entry.Metrics.CpuTimeMs)
		fmt.Printf("Memory Peak:  %.2f MB\n", entry.Metrics.MemoryPeakMb)
		fmt.Printf("Success:      %t\n", entry.Metrics.Success)
		if len(entry.Tags) > 0 {
			fmt.Printf("Tags:         %v\n", entry.Tags)
		}
	}
}

func readCodeFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("code file path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func splitTags(s string) []string {
	var result []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			result = append(result, t)
		}
	}
	return result
}

func compareProfiles(ctx context.Context, client pb.PerformanceProfilerClient) {
	if *oldProfile == "" || *newProfile == "" {
		log.Fatal("Both --old-profile and --new-profile are required for compare mode")
	}

	oldSource, _ := readCodeFile(*oldCode)
	newSource, _ := readCodeFile(*newCode)

	req := &pb.CompareProfilesRequest{
		OldProfileId:        *oldProfile,
		NewProfileId:        *newProfile,
		OldSourceCode:       oldSource,
		NewSourceCode:       newSource,
		SignificanceThreshold: *significance,
	}

	log.Printf("Comparing profiles: %s vs %s...", (*oldProfile)[:8], (*newProfile)[:8])
	start := time.Now()

	resp, err := client.CompareProfiles(ctx, req)
	if err != nil {
		log.Fatalf("Comparison failed: %v", err)
	}

	elapsed := time.Since(start)
	log.Printf("Comparison completed in %v", elapsed)

	fmt.Println("\n=== Performance Comparison ===")
	fmt.Printf("Old Profile:   %s\n", resp.OldProfileId)
	fmt.Printf("New Profile:   %s\n", resp.NewProfileId)
	fmt.Printf("Time Delta:    %s\n", formatDuration(resp.TimeDeltaSeconds))
	fmt.Printf("Trend:         %s\n", formatTrend(resp.PerformanceTrend))
	fmt.Printf("\nSummary:       %s\n", resp.OverallSummary)

	fmt.Println("\n--- Metric Differences ---")
	for _, diff := range resp.MetricDiffs {
		sigMark := ""
		if diff.IsSignificant {
			sigMark = " *"
		}

		changeMark := ""
		switch diff.ChangeType {
		case "regression":
			changeMark = "↑"
		case "improvement":
			changeMark = "↓"
		}

		fmt.Printf("  %-20s: %10.2f → %10.2f  diff: %+10.2f (%+7.1f%%) %s%s\n",
			diff.MetricName,
			diff.OldValue,
			diff.NewValue,
			diff.AbsoluteDiff,
			diff.PercentageDiff,
			changeMark,
			sigMark,
		)
	}

	fmt.Println("\n* = statistically significant change")

	if len(resp.RegressionTags) > 0 {
		fmt.Println("\nRegression Tags:")
		for _, tag := range resp.RegressionTags {
			fmt.Printf("  - %s\n", tag)
		}
	}
	if len(resp.ImprovementTags) > 0 {
		fmt.Println("\nImprovement Tags:")
		for _, tag := range resp.ImprovementTags {
			fmt.Printf("  - %s\n", tag)
		}
	}

	if len(resp.SourceDiffs) > 0 {
		fmt.Println("\n--- Source Code Differences ---")
		for _, diff := range resp.SourceDiffs {
			prefix := "  "
			switch diff.LineType {
			case "added":
				prefix = "+ "
			case "removed":
				prefix = "- "
			}
			impactStr := ""
			if diff.PerformanceImpactScore != 0 {
				impactStr = fmt.Sprintf(" [impact: %.1f]", diff.PerformanceImpactScore)
			}
			fmt.Printf("%sLine %d:%s %s\n", prefix, diff.LineNumber, impactStr, diff.DiffHint)
			if diff.LineType == "added" && diff.NewLine != "" {
				fmt.Printf("    + %s\n", diff.NewLine)
			}
			if diff.LineType == "removed" && diff.OldLine != "" {
				fmt.Printf("    - %s\n", diff.OldLine)
			}
			if diff.DiffHint != "" {
				fmt.Printf("      %s\n", diff.DiffHint)
			}
			fmt.Println()
		}
	}
}

func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%d seconds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%d minutes", seconds/60)
	}
	if seconds < 86400 {
		return fmt.Sprintf("%d hours", seconds/3600)
	}
	return fmt.Sprintf("%d days", seconds/86400)
}

func formatTrend(trend string) string {
	colors := map[string]string{
		"improved":  "↓ IMPROVED",
		"regressed": "↑ REGRESSED",
		"stable":    "→ STABLE",
	}
	if t, ok := colors[trend]; ok {
		return t
	}
	return trend
}
