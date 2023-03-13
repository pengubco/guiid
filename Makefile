
generate-proto: api/v1/snowflake.proto
	protoc api/v1/snowflake.proto --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative

build-go:
	go build -o ./build/guiid ./cmd

build-docker:
	DOCKER_BUILDKIT=1 docker build . -t guiid
