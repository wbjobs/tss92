.PHONY: proto build server client test up down clean

proto:
	@echo "Generating protobuf code..."
	@protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/profiler.proto

build:
	@echo "Building..."
	@go build -o bin/profiler-server ./cmd/server
	@go build -o bin/profiler-client ./cmd/client

server:
	@echo "Starting server..."
	@go run ./cmd/server

client:
	@echo "Running client..."
	@go run ./cmd/client $(ARGS)

test:
	@echo "Running tests..."
	@go test -v ./internal/...

up:
	@echo "Starting services..."
	@docker-compose up -d

down:
	@echo "Stopping services..."
	@docker-compose down

clean:
	@echo "Cleaning..."
	@rm -rf bin/
	@docker-compose down -v

example-python:
	@go run ./cmd/client -lang python -code examples/python_sample.py -tags "demo,python"

example-java:
	@go run ./cmd/client -lang java -code examples/java_sample.java -tags "demo,java"

example-go:
	@go run ./cmd/client -lang go -code examples/go_sample.go -tags "demo,go"

query-high-cpu:
	@go run ./cmd/client -query -query-tags "高CPU"

query-demo:
	@go run ./cmd/client -query -query-tags "demo"
