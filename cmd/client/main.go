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
	query    = flag.Bool("query", false, "Query profiles instead of profiling")
	queryTags = flag.String("query-tags", "", "Tags to query (comma-separated)")
	limit    = flag.Int("limit", 10, "Query limit")
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

	if *query {
		queryProfiles(ctx, client)
		return
	}

	profileCode(ctx, client)
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
