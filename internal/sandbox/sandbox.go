package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"
)

type ResourceLimits struct {
	MemoryMB        int64
	CPUs            float64
	CPUQuotaPercent int
	PidsLimit       int
	IOReadBPS       int64
	IOWriteBPS      int64
	BlkioWeight     uint16
	NoFileSoft      int
	NoFileHard      int
	OOMScoreAdj     int
	EnableOOMKill   bool
}

func DefaultResourceLimits() *ResourceLimits {
	return &ResourceLimits{
		MemoryMB:        512,
		CPUs:            1.0,
		CPUQuotaPercent: 100,
		PidsLimit:       256,
		IOReadBPS:       50 * 1024 * 1024,
		IOWriteBPS:      50 * 1024 * 1024,
		BlkioWeight:     500,
		NoFileSoft:      1024,
		NoFileHard:      2048,
		OOMScoreAdj:     1000,
		EnableOOMKill:   true,
	}
}

type ExecutionResult struct {
	Stdout         string
	Stderr         string
	ExitCode       int
	Duration       time.Duration
	RawDuration    time.Duration
	AdjustedCPUTime float64
	ResourceData   *ResourceMetrics
	CalibrationFactor float64
	KilledByOOM    bool
	TimedOut       bool
}

type ResourceMetrics struct {
	CPUTotal     float64
	MemoryPeak   float64
	IOReadBytes  int64
	IOWriteBytes int64
}

type CalibrationData struct {
	mu              sync.RWMutex
	baselineCPUTime map[string][]float64
	baselineWallTime map[string][]float64
	lastCalibrated  map[string]time.Time
	factors         map[string]*CalibrationFactor
	windowSize      int
}

type CalibrationFactor struct {
	CPUBaseline    float64
	WallBaseline   float64
	LoadFactor     float64
	LastUpdated    time.Time
}

func NewCalibrationData(windowSize int) *CalibrationData {
	if windowSize <= 0 {
		windowSize = 10
	}
	return &CalibrationData{
		baselineCPUTime:  make(map[string][]float64),
		baselineWallTime: make(map[string][]float64),
		lastCalibrated:   make(map[string]time.Time),
		factors:          make(map[string]*CalibrationFactor),
		windowSize:       windowSize,
	}
}

func (c *CalibrationData) Record(language string, cpuMs, wallMs float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.baselineCPUTime[language] = append(c.baselineCPUTime[language], cpuMs)
	c.baselineWallTime[language] = append(c.baselineWallTime[language], wallMs)

	if len(c.baselineCPUTime[language]) > c.windowSize {
		c.baselineCPUTime[language] = c.baselineCPUTime[language][len(c.baselineCPUTime[language])-c.windowSize:]
	}
	if len(c.baselineWallTime[language]) > c.windowSize {
		c.baselineWallTime[language] = c.baselineWallTime[language][len(c.baselineWallTime[language])-c.windowSize:]
	}

	c.lastCalibrated[language] = time.Now()
	c.recomputeFactorLocked(language)
}

func (c *CalibrationData) recomputeFactorLocked(language string) {
	cpuSamples := c.baselineCPUTime[language]
	wallSamples := c.baselineWallTime[language]

	if len(cpuSamples) < 3 {
		c.factors[language] = &CalibrationFactor{
			CPUBaseline:  0,
			WallBaseline: 0,
			LoadFactor:   1.0,
			LastUpdated:  time.Now(),
		}
		return
	}

	cpuMedian := median(cpuSamples)
	wallMedian := median(wallSamples)

	loadFactor := 1.0
	if len(cpuSamples) >= 5 {
		recentAvg := avg(cpuSamples[len(cpuSamples)-3:])
		historicalAvg := avg(cpuSamples[:len(cpuSamples)-2])
		if historicalAvg > 0 {
			loadFactor = recentAvg / historicalAvg
			loadFactor = math.Max(0.5, math.Min(3.0, loadFactor))
		}
	}

	c.factors[language] = &CalibrationFactor{
		CPUBaseline:    cpuMedian,
		WallBaseline:   wallMedian,
		LoadFactor:     loadFactor,
		LastUpdated:    time.Now(),
	}
}

func (c *CalibrationData) GetFactor(language string) *CalibrationFactor {
	c.mu.RLock()
	defer c.mu.RUnlock()

	f, ok := c.factors[language]
	if !ok {
		return &CalibrationFactor{
			CPUBaseline:   0,
			WallBaseline:  0,
			LoadFactor:    1.0,
			LastUpdated:   time.Time{},
		}
	}
	return f
}

func (c *CalibrationData) NeedsCalibration(language string, interval time.Duration) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	t, ok := c.lastCalibrated[language]
	if !ok {
		return true
	}
	if len(c.baselineCPUTime[language]) < 5 {
		return true
	}
	return time.Since(t) > interval
}

func median(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func avg(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range samples {
		sum += s
	}
	return sum / float64(len(samples))
}

type SandboxConfig struct {
	Limits              *ResourceLimits
	MaxConcurrency      int64
	CalibrationRounds   int
	CalibrationInterval time.Duration
	TimeoutBuffer       time.Duration
}

func DefaultConfig() *SandboxConfig {
	return &SandboxConfig{
		Limits:              DefaultResourceLimits(),
		MaxConcurrency:      2,
		CalibrationRounds:   5,
		CalibrationInterval: 5 * time.Minute,
		TimeoutBuffer:       5 * time.Second,
	}
}

type Sandbox struct {
	tempDir      string
	config       *SandboxConfig
	sem          *semaphore.Weighted
	calibration  *CalibrationData
	activeJobs   int64
	totalJobs    int64
}

func NewSandbox() (*Sandbox, error) {
	return NewSandboxWithConfig(DefaultConfig())
}

func NewSandboxWithConfig(cfg *SandboxConfig) (*Sandbox, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.Limits == nil {
		cfg.Limits = DefaultResourceLimits()
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 2
	}
	if cfg.CalibrationRounds <= 0 {
		cfg.CalibrationRounds = 5
	}
	if cfg.TimeoutBuffer <= 0 {
		cfg.TimeoutBuffer = 5 * time.Second
	}

	tempDir, err := os.MkdirTemp("", "profiler-sandbox-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err := checkDockerAvailable(); err != nil {
		os.RemoveAll(tempDir)
		return nil, err
	}

	sb := &Sandbox{
		tempDir:     tempDir,
		config:      cfg,
		sem:         semaphore.NewWeighted(cfg.MaxConcurrency),
		calibration: NewCalibrationData(20),
	}

	return sb, nil
}

func (s *Sandbox) Config() *SandboxConfig {
	return s.config
}

func (s *Sandbox) Stats() (active, total int64) {
	return atomic.LoadInt64(&s.activeJobs), atomic.LoadInt64(&s.totalJobs)
}

func checkDockerAvailable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker is not available: %w", err)
	}
	return nil
}

func (s *Sandbox) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	entries, _ := os.ReadDir(s.tempDir)
	for _, e := range entries {
		name := e.Name()
		killCmd := exec.CommandContext(ctx, "docker", "rm", "-f", name)
		killCmd.Run()
	}
	return os.RemoveAll(s.tempDir)
}

func (s *Sandbox) Warmup(ctx context.Context, languages []string) error {
	for _, lang := range languages {
		if err := s.calibrate(ctx, lang, s.config.CalibrationRounds); err != nil {
			return fmt.Errorf("warmup failed for %s: %w", lang, err)
		}
	}
	return nil
}

func (s *Sandbox) calibrate(ctx context.Context, language string, rounds int) error {
	var benchmarkCode string
	switch language {
	case "python":
		benchmarkCode = `
def profile_target():
    s = 0
    for i in range(1000):
        s += i * i
    return s
`
	case "java":
		benchmarkCode = `
public class UserCode {
    public static Object profileTarget() {
        long s = 0;
        for (int i = 0; i < 1000; i++) {
            s += (long)i * i;
        }
        return s;
    }
}
`
	case "go":
		benchmarkCode = `
func profileTarget() interface{} {
	s := int64(0)
	for i := int64(0); i < 1000; i++ {
		s += i * i
	}
	return s
}
`
	default:
		return fmt.Errorf("unsupported language: %s", language)
	}

	for i := 0; i < rounds; i++ {
		result, err := s.executeInternal(ctx, language, benchmarkCode, 30*time.Second, true)
		if err != nil {
			continue
		}
		if result.ResourceData != nil {
			s.calibration.Record(
				language,
				result.ResourceData.CPUTotal,
				float64(result.RawDuration.Milliseconds()),
			)
		}
	}
	return nil
}

func (s *Sandbox) Execute(ctx context.Context, language, code string, timeout time.Duration) (*ExecutionResult, error) {
	if s.calibration.NeedsCalibration(language, s.config.CalibrationInterval) {
		go s.calibrate(context.Background(), language, 3)
	}

	if err := s.sem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("failed to acquire semaphore: %w", err)
	}
	defer s.sem.Release(1)

	atomic.AddInt64(&s.activeJobs, 1)
	atomic.AddInt64(&s.totalJobs, 1)
	defer atomic.AddInt64(&s.activeJobs, -1)

	result, err := s.executeInternal(ctx, language, code, timeout, false)
	if err != nil {
		return nil, err
	}

	factor := s.calibration.GetFactor(language)
	result.CalibrationFactor = factor.LoadFactor

	if result.ResourceData != nil && factor.LoadFactor > 0 {
		adjusted := result.ResourceData.CPUTotal / factor.LoadFactor
		result.AdjustedCPUTime = math.Max(0, adjusted-factor.CPUBaseline)
	}

	return result, nil
}

func (s *Sandbox) executeInternal(ctx context.Context, language, code string, timeout time.Duration, isCalibration bool) (*ExecutionResult, error) {
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

	if !isCalibration || language != "go" {
		if err := os.WriteFile(filepath.Join(jobDir, config.SourceFile), []byte(code), 0644); err != nil {
			return nil, fmt.Errorf("failed to write source file: %w", err)
		}
	}

	hardTimeout := timeout + s.config.TimeoutBuffer
	execCtx, cancel := context.WithTimeout(ctx, hardTimeout)
	defer cancel()

	if !isCalibration {
		pullCtx, pullCancel := context.WithTimeout(execCtx, 2*time.Minute)
		pullCmd := exec.CommandContext(pullCtx, "docker", "pull", "-q", config.Image)
		pullCmd.Run()
		pullCancel()
	}

	args := s.buildDockerArgs(jobID, jobDir, config)

	statsCtx, statsCancel := context.WithCancel(execCtx)
	defer statsCancel()
	statsChan := make(chan *ResourceMetrics, 1)
	go s.collectStats(statsCtx, jobID, statsChan)

	cleanup := func() {
		killCtx, kc := context.WithTimeout(context.Background(), 5*time.Second)
		defer kc()
		exec.CommandContext(killCtx, "docker", "rm", "-f", jobID).Run()
	}
	defer cleanup()

	startTime := time.Now()
	cmd := exec.CommandContext(execCtx, "docker", args...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	rawDuration := time.Since(startTime)
	statsCancel()

	exitCode := 0
	killedByOOM := false
	timedOut := false

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if execCtx.Err() == context.DeadlineExceeded {
			timedOut = true
			exitCode = -1
		} else if ctx.Err() == context.Canceled {
			return nil, fmt.Errorf("execution canceled: %w", err)
		} else {
			return nil, fmt.Errorf("execution failed: %w", err)
		}
	}

	if exitCode == 137 || strings.Contains(stderr.String(), "Killed") || strings.Contains(stderr.String(), "OOM") {
		killedByOOM = true
	}

	var resourceData *ResourceMetrics
	select {
	case rd := <-statsChan:
		resourceData = rd
	case <-time.After(500 * time.Millisecond):
		resourceData = &ResourceMetrics{}
	}

	actualDuration := rawDuration
	if timedOut {
		actualDuration = timeout
	}

	return &ExecutionResult{
		Stdout:         stdout.String(),
		Stderr:         stderr.String(),
		ExitCode:       exitCode,
		Duration:       actualDuration,
		RawDuration:    rawDuration,
		ResourceData:   resourceData,
		KilledByOOM:    killedByOOM,
		TimedOut:       timedOut,
	}, nil
}

func (s *Sandbox) buildDockerArgs(jobID, jobDir string, config *executionConfig) []string {
	limits := s.config.Limits

	args := []string{
		"run",
		"--rm",
		"--name", jobID,
		"--network", "none",
		"--read-only",
		"--tmpfs", "/tmp:rw,size=64m",
		"-v", fmt.Sprintf("%s:/app:ro", jobDir),
		"-w", "/app",
		"--stop-signal", "SIGKILL",
		"--stop-timeout", "2",

		"--memory", fmt.Sprintf("%dm", limits.MemoryMB),
		"--memory-swap", fmt.Sprintf("%dm", limits.MemoryMB),
		"--memory-swappiness", "0",
		"--kernel-memory", fmt.Sprintf("%dm", limits.MemoryMB/4),

		"--cpus", fmt.Sprintf("%.2f", limits.CPUs),
		"--cpu-shares", fmt.Sprintf("%d", int(limits.CPUs*1024)),
		"--pids-limit", fmt.Sprintf("%d", limits.PidsLimit),

		"--blkio-weight", fmt.Sprintf("%d", limits.BlkioWeight),

		"--ulimit", fmt.Sprintf("nofile=%d:%d", limits.NoFileSoft, limits.NoFileHard),
		"--ulimit", fmt.Sprintf("nproc=%d:%d", limits.PidsLimit, limits.PidsLimit),
		"--ulimit", "core=0:0",

		"--oom-score-adj", fmt.Sprintf("%d", limits.OOMScoreAdj),

		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--privileged=false",
		"--userns", "host",
	}

	if !limits.EnableOOMKill {
		args = append(args, "--oom-kill-disable")
	}

	args = append(args, config.Image)
	args = append(args, config.Cmd...)

	return args
}

func (s *Sandbox) collectStats(ctx context.Context, containerName string, resultChan chan<- *ResourceMetrics) {
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
			cmd := exec.CommandContext(ctx, "docker", "stats", containerName,
				"--format", "{{json .}}",
				"--no-stream",
			)
			output, err := cmd.Output()
			if err != nil {
				continue
			}

			var stats struct {
				CPUPerc  string `json:"CPUPerc"`
				MemUsage string `json:"MemUsage"`
				BlockIO  string `json:"BlockIO"`
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
	Image      string
	Cmd        []string
	SourceFile string
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
			Cmd:        []string{"sh", "-c", "javac /app/Wrapper.java /app/UserCode.java && java -XX:+ExitOnOutOfMemoryError -Xmx256m -cp /app Wrapper"},
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
import signal

def timeout_handler(signum, frame):
    print("PROFILE_STATS: {\"success\": false, \"error\": \"timeout\"}", flush=True)
    sys.exit(124)

def main():
    signal.signal(signal.SIGALRM, timeout_handler)
    timeout = int(sys.argv[2]) if len(sys.argv) > 2 else 0
    if timeout > 0:
        signal.alarm(timeout)

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

    print(f"PROFILE_STATS: {json.dumps(stats)}", flush=True)

if __name__ == "__main__":
    main()
`

const javaWrapper = `
import java.lang.management.*;
import java.io.*;
import java.lang.reflect.Method;
import java.util.concurrent.*;

public class Wrapper {
    public static void main(String[] args) {
        ExecutorService executor = Executors.newSingleThreadExecutor();
        Future<?> future = executor.submit(() -> {
            runProfiling();
            return null;
        });

        try {
            future.get(25, TimeUnit.SECONDS);
        } catch (TimeoutException e) {
            future.cancel(true);
            System.out.println("PROFILE_STATS: {\"success\": false, \"error\": \"timeout\"}");
            System.exit(124);
        } catch (Exception e) {
            String errorStats = String.format(
                "{\"success\": false, \"error\": \"%s\"}",
                e.getMessage() != null ? e.getMessage().replace("\"", "\\\"") : "unknown"
            );
            System.out.println("PROFILE_STATS: " + errorStats);
        } finally {
            executor.shutdownNow();
        }
    }

    private static void runProfiling() {
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

            MemoryUsage heapAfter = memoryBean.getHeapMemoryUsage();

            String stats = String.format(
                "{\"cpu_time_ms\": %.2f, \"wall_time_ms\": %.2f, \"memory_peak_bytes\": %d, \"gc_count\": %d, \"gc_time_ms\": %d, \"success\": true}",
                (endCpu - startCpu) / 1_000_000.0,
                (endTime - startTime) / 1_000_000.0,
                heapAfter.getUsed(),
                endGcCount - startGcCount,
                endGcTime - startGcTime
            );
            System.out.println("PROFILE_RESULT: " + result);
            System.out.println("PROFILE_STATS: " + stats);

        } catch (Exception e) {
            String errorStats = String.format(
                "{\"success\": false, \"error\": \"%s\"}",
                e.getMessage() != null ? e.getMessage().replace("\"", "\\\"") : "unknown"
            );
            System.out.println("PROFILE_STATS: " + errorStats);
            e.printStackTrace(System.err);
        }
    }
}
`

func generateGoWrapper(userCode string) string {
	return `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"
)

` + userCode + `

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	done := make(chan struct{})
	var result interface{}
	var panicErr interface{}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicErr = r
			}
			close(done)
		}()
		runtime.GC()
		debug.FreeOSMemory()

		var m1, m2 runtime.MemStats
		runtime.ReadMemStats(&m1)

		start := time.Now()

		var gcStart debug.GCStats
		runtime.ReadGCStats(&gcStart)

		result = profileTarget()

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
	}()

	select {
	case <-done:
		if panicErr != nil {
			errorStats := fmt.Sprintf(
				"{\"success\": false, \"error\": \"panic: %v\"}",
				panicErr,
			)
			fmt.Println("PROFILE_STATS:", errorStats)
			os.Exit(1)
		}
	case <-ctx.Done():
		fmt.Println("PROFILE_STATS: {\"success\": false, \"error\": \"timeout\"}")
		os.Exit(124)
	}
}
`
}
