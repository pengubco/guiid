
generate-proto: api/v1/snowflake.proto
	protoc api/v1/snowflake.proto --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative

build-go:
	golangci-lint run
	CGO_ENABLED=0 go build -ldflags="-w -s \
    -X main.Version=v1.0.0 \
    -X main.BuildTime=$(date -u '+%Y-%m-%d_%H:%M:%S') \
    -X main.GitCommit=$(git rev-parse HEAD)" \
    -trimpath -o ./build/i3d ./cmd

build-docker:
	DOCKER_BUILDKIT=1 docker build . -t i3d
