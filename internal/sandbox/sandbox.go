package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ExecutionResult struct {
	Stdout       string
	Stderr       string
	ExitCode     int
	Duration     time.Duration
	ResourceData *ResourceMetrics
}

type ResourceMetrics struct {
	CPUTotal     float64
	MemoryPeak   float64
	IOReadBytes  int64
	IOWriteBytes int64
}

type Sandbox struct {
	tempDir string
	mu      sync.Mutex
}

func NewSandbox() (*Sandbox, error) {
	tempDir, err := os.MkdirTemp("", "profiler-sandbox-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err := checkDockerAvailable(); err != nil {
		return nil, err
	}

	return &Sandbox{
		tempDir: tempDir,
	}, nil
}

func checkDockerAvailable() error {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker is not available: %w", err)
	}
	return nil
}

func (s *Sandbox) Close() error {
	return os.RemoveAll(s.tempDir)
}

func (s *Sandbox) Execute(ctx context.Context, language, code string, timeout time.Duration) (*ExecutionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobID := uuid.New().String()
	jobDir := filepath.Join(s.tempDir, jobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create job dir: %w", err)
	}
	defer os.RemoveAll(jobDir)

	config, err := s.getExecutionConfig(language, code, jobDir)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(filepath.Join(jobDir, config.SourceFile), []byte(code), 0644); err != nil {
		return nil, fmt.Errorf("failed to write source file: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pullCmd := exec.CommandContext(execCtx, "docker", "pull", config.Image)
	pullCmd.Run()

	statsCtx, statsCancel := context.WithCancel(execCtx)
	statsChan := make(chan *ResourceMetrics, 1)
	go s.collectStats(statsCtx, jobID, statsChan)

	args := []string{
		"run",
		"--rm",
		"--name", jobID,
		"--network", "none",
		"--memory", "512m",
		"--cpus", "1",
		"--pids-limit", "256",
		"--read-only",
		"--tmpfs", "/tmp:rw,size=64m",
		"-v", fmt.Sprintf("%s:/app:ro", jobDir),
		"-w", "/app",
		"--stop-signal", "SIGKILL",
	}
	args = append(args, config.Image)
	args = append(args, config.Cmd...)

	startTime := time.Now()
	cmd := exec.CommandContext(execCtx, "docker", args...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	duration := time.Since(startTime)
	statsCancel()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("execution failed: %w", err)
		}
	}

	var resourceData *ResourceMetrics
	select {
	case rd := <-statsChan:
		resourceData = rd
	default:
		resourceData = &ResourceMetrics{}
	}

	return &ExecutionResult{
		Stdout:       stdout.String(),
		Stderr:       stderr.String(),
		ExitCode:     exitCode,
		Duration:     duration,
		ResourceData: resourceData,
	}, nil
}

func (s *Sandbox) collectStats(ctx context.Context, containerName string, resultChan chan<- *ResourceMetrics) {
	cmd := exec.CommandContext(ctx, "docker", "stats", containerName,
		"--format", "{{json .}}",
		"--no-stream",
	)

	var peakMem float64
	var totalCPU float64
	var readBytes, writeBytes int64

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			resultChan <- &ResourceMetrics{
				CPUTotal:     totalCPU,
				MemoryPeak:   peakMem,
				IOReadBytes:  readBytes,
				IOWriteBytes: writeBytes,
			}
			return
		case <-ticker.C:
			output, err := cmd.Output()
			if err != nil {
				continue
			}

			var stats struct {
				CPUPerc    string `json:"CPUPerc"`
				MemUsage   string `json:"MemUsage"`
				BlockIO    string `json:"BlockIO"`
			}

			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if err := json.Unmarshal([]byte(line), &stats); err != nil {
					continue
				}

				if mem, ok := parseMemory(stats.MemUsage); ok && mem > peakMem {
					peakMem = mem
				}
				if cpu, ok := parseCPU(stats.CPUPerc); ok {
					totalCPU += cpu * 0.1
				}
				if rb, wb, ok := parseBlockIO(stats.BlockIO); ok {
					readBytes = rb
					writeBytes = wb
				}
			}
		}
	}
}

func parseMemory(s string) (float64, bool) {
	parts := strings.Split(s, "/")
	if len(parts) < 1 {
		return 0, false
	}
	val := strings.TrimSpace(parts[0])
	var value float64
	var unit string
	fmt.Sscanf(val, "%f%s", &value, &unit)

	switch strings.ToLower(unit) {
	case "mib", "mb":
		return value, true
	case "gib", "gb":
		return value * 1024, true
	case "kib", "kb":
		return value / 1024, true
	case "b":
		return value / (1024 * 1024), true
	}
	return 0, false
}

func parseCPU(s string) (float64, bool) {
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSpace(s)
	var value float64
	if _, err := fmt.Sscanf(s, "%f", &value); err != nil {
		return 0, false
	}
	return value, true
}

func parseBlockIO(s string) (int64, int64, bool) {
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return 0, 0, false
	}

	readPart := strings.TrimSpace(parts[0])
	writePart := strings.TrimSpace(parts[1])

	readBytes := parseSize(readPart)
	writeBytes := parseSize(writePart)

	return readBytes, writeBytes, true
}

func parseSize(s string) int64 {
	var value float64
	var unit string
	fmt.Sscanf(s, "%f%s", &value, &unit)

	switch strings.ToLower(unit) {
	case "mb":
		return int64(value * 1024 * 1024)
	case "gb":
		return int64(value * 1024 * 1024 * 1024)
	case "kb":
		return int64(value * 1024)
	case "b":
		return int64(value)
	}
	return int64(value)
}

type executionConfig struct {
	Image        string
	Cmd          []string
	SourceFile   string
}

func (s *Sandbox) getExecutionConfig(language, code, jobDir string) (*executionConfig, error) {
	switch language {
	case "python":
		if err := os.WriteFile(filepath.Join(jobDir, "wrapper.py"), []byte(pythonWrapper), 0644); err != nil {
			return nil, err
		}
		return &executionConfig{
			Image:      "python:3.11-slim",
			Cmd:        []string{"python", "/app/wrapper.py", "user_code.py"},
			SourceFile: "user_code.py",
		}, nil
	case "java":
		if err := os.WriteFile(filepath.Join(jobDir, "Wrapper.java"), []byte(javaWrapper), 0644); err != nil {
			return nil, err
		}
		return &executionConfig{
			Image:      "eclipse-temurin:17-jre",
			Cmd:        []string{"sh", "-c", "javac /app/Wrapper.java /app/UserCode.java && java -cp /app Wrapper"},
			SourceFile: "UserCode.java",
		}, nil
	case "go":
		wrapperCode := generateGoWrapper(code)
		if err := os.WriteFile(filepath.Join(jobDir, "wrapper.go"), []byte(wrapperCode), 0644); err != nil {
			return nil, err
		}
		return &executionConfig{
			Image:      "golang:1.21-alpine",
			Cmd:        []string{"sh", "-c", "cd /app && go run wrapper.go"},
			SourceFile: "wrapper.go",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported language: %s", language)
	}
}

const pythonWrapper = `
import sys
import time
import tracemalloc
import gc
import json
from io import StringIO

def main():
    code_file = sys.argv[1]
    
    with open(code_file, 'r') as f:
        user_code = f.read()
    
    tracemalloc.start()
    gc.collect()
    gc.disable()
    
    start_time = time.perf_counter()
    start_cpu = time.process_time()
    
    try:
        exec_globals = {}
        exec(user_code, exec_globals)
        
        if 'profile_target' in exec_globals:
            result = exec_globals['profile_target']()
            print(f"PROFILE_RESULT: {result}")
        
        success = True
        error_msg = ""
    except Exception as e:
        success = False
        error_msg = str(e)
        print(f"PROFILE_ERROR: {error_msg}", file=sys.stderr)
    
    end_cpu = time.process_time()
    end_time = time.perf_counter()
    
    gc.enable()
    gc.collect()
    
    current, peak = tracemalloc.get_traced_memory()
    tracemalloc.stop()
    
    stats = {
        "cpu_time_ms": (end_cpu - start_cpu) * 1000,
        "wall_time_ms": (end_time - start_time) * 1000,
        "memory_peak_bytes": peak,
        "gc_count": gc.get_count()[0],
        "success": success,
        "error": error_msg
    }
    
    print(f"PROFILE_STATS: {json.dumps(stats)}")

if __name__ == "__main__":
    main()
`

const javaWrapper = `
import java.lang.management.*;
import java.io.*;
import java.lang.reflect.Method;

public class Wrapper {
    public static void main(String[] args) {
        try {
            MemoryMXBean memoryBean = ManagementFactory.getMemoryMXBean();
            ThreadMXBean threadBean = ManagementFactory.getThreadMXBean();
            GarbageCollectorMXBean gcBean = null;
            for (GarbageCollectorMXBean b : ManagementFactory.getGarbageCollectorMXBeans()) {
                gcBean = b;
                break;
            }
            
            long startCpu = threadBean.getCurrentThreadCpuTime();
            long startGcCount = gcBean != null ? gcBean.getCollectionCount() : 0;
            long startGcTime = gcBean != null ? gcBean.getCollectionTime() : 0;
            
            System.gc();
            
            long startTime = System.nanoTime();
            
            Class<?> userClass = Class.forName("UserCode");
            Object instance = userClass.getDeclaredConstructor().newInstance();
            Method targetMethod = userClass.getMethod("profileTarget");
            Object result = targetMethod.invoke(instance);
            
            long endTime = System.nanoTime();
            long endCpu = threadBean.getCurrentThreadCpuTime();
            long endGcCount = gcBean != null ? gcBean.getCollectionCount() : 0;
            long endGcTime = gcBean != null ? gcBean.getCollectionTime() : 0;
            long endMem = memoryBean.getHeapMemoryUsage().getMax();
            
            String stats = String.format(
                "{\"cpu_time_ms\": %.2f, \"wall_time_ms\": %.2f, \"memory_peak_bytes\": %d, \"gc_count\": %d, \"gc_time_ms\": %d, \"success\": true}",
                (endCpu - startCpu) / 1_000_000.0,
                (endTime - startTime) / 1_000_000.0,
                endMem,
                endGcCount - startGcCount,
                endGcTime - startGcTime
            );
            System.out.println("PROFILE_RESULT: " + result);
            System.out.println("PROFILE_STATS: " + stats);
            
        } catch (Exception e) {
            String errorStats = String.format(
                "{\"success\": false, \"error\": \"%s\"}",
                e.getMessage().replace("\"", "\\\"")
            );
            System.out.println("PROFILE_STATS: " + errorStats);
            e.printStackTrace();
        }
    }
}
`

func generateGoWrapper(userCode string) string {
	return `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"
)

` + userCode + `

func main() {
	runtime.GC()
	debug.FreeOSMemory()
	
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)
	
	start := time.Now()
	
	var gcStart debug.GCStats
	runtime.ReadGCStats(&gcStart)
	
	result := profileTarget()
	
	elapsed := time.Since(start)
	
	runtime.ReadMemStats(&m2)
	var gcEnd debug.GCStats
	runtime.ReadGCStats(&gcEnd)
	
	stats := map[string]interface{}{
		"cpu_time_ms":       float64(elapsed.Nanoseconds()) / 1e6,
		"wall_time_ms":      float64(elapsed.Nanoseconds()) / 1e6,
		"memory_peak_bytes": m2.TotalAlloc - m1.TotalAlloc,
		"gc_count":          gcEnd.NumGC - gcStart.NumGC,
		"gc_time_ms":        float64(gcEnd.PauseTotal - gcStart.PauseTotal) / 1e6,
		"success":           true,
	}
	
	statsJSON, _ := json.Marshal(stats)
	fmt.Println("PROFILE_RESULT:", result)
	fmt.Println("PROFILE_STATS:", string(statsJSON))
}
`
}
