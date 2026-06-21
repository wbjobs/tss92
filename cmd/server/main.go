package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"profiler/internal/profiler"
	"profiler/internal/sandbox"
	"profiler/internal/storage"
	pb "profiler/proto"
)

var (
	port         = flag.Int("port", 50051, "gRPC server port")
	influxURL    = flag.String("influx-url", getEnv("INFLUXDB_URL", "http://localhost:8086"), "InfluxDB URL")
	influxToken  = flag.String("influx-token", getEnv("INFLUXDB_TOKEN", ""), "InfluxDB token")
	influxOrg    = flag.String("influx-org", getEnv("INFLUXDB_ORG", "profiler"), "InfluxDB organization")
	influxBucket = flag.String("influx-bucket", getEnv("INFLUXDB_BUCKET", "profiles"), "InfluxDB bucket")
)

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

type server struct {
	pb.UnimplementedPerformanceProfilerServer
	profiler *profiler.PerformanceProfiler
}

func (s *server) ProfileCode(ctx context.Context, req *pb.ProfileRequest) (*pb.ProfileResponse, error) {
	return s.profiler.ProfileCode(ctx, req)
}

func (s *server) QueryProfiles(ctx context.Context, req *pb.ProfileQueryRequest) (*pb.ProfileQueryResponse, error) {
	return s.profiler.QueryProfiles(ctx, req)
}

func main() {
	flag.Parse()

	log.Println("Initializing Performance Profiler Service...")

	sb, err := sandbox.NewSandbox()
	if err != nil {
		log.Fatalf("Failed to initialize sandbox: %v", err)
	}
	defer sb.Close()
	log.Println("Sandbox initialized successfully")

	st := storage.NewStorage(*influxURL, *influxToken, *influxOrg, *influxBucket)
	defer st.Close()
	log.Println("Storage initialized successfully")

	p := profiler.NewPerformanceProfiler(sb, st)
	log.Println("Profiler initialized successfully")

	warmupCtx, warmupCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	go func() {
		defer warmupCancel()
		log.Println("Starting warmup calibration for all languages...")
		if err := sb.Warmup(warmupCtx, []string{"python", "java", "go"}); err != nil {
			log.Printf("Warmup completed with warnings: %v", err)
		} else {
			log.Println("Warmup calibration completed successfully")
		}
	}()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(10*1024*1024),
		grpc.MaxSendMsgSize(10*1024*1024),
	)

	pb.RegisterPerformanceProfilerServer(grpcServer, &server{profiler: p})

	reflection.Register(grpcServer)

	log.Printf("gRPC server listening on port %d", *port)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down server...")
	grpcServer.GracefulStop()
	log.Println("Server stopped gracefully")
}
