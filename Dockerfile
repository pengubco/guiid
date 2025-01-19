#syntax=docker/dockerfile:latest
FROM golang:1.23-alpine AS build

# disable CGO because of the cross-compiling: compile on alpine and run on scratch.
ENV CGO_ENABLED=0

WORKDIR /usr/src/i3d

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
  CGO_ENABLED=0 go build -ldflags="-w -s \
  -X main.Version=v1.0.0 \
  -X main.BuildTime=$(date -u '+%Y-%m-%d_%H:%M:%S') \
  -X main.GitCommit=$(git rev-parse HEAD)" \
  -trimpath \
  go build -o ./i3d ./cmd

FROM scratch
ENV PATH=/bin
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
COPY --from=build /usr/src/i3d/i3d /bin/
USER nobody:nobody
EXPOSE 7669 8001
ENTRYPOINT ["/bin/i3d"]
