#syntax=docker/dockerfile:latest
FROM golang:1.19-alpine AS build

# disable CGO because of the cross-compiling: compile on alpine and run on scratch.
ENV CGO_ENABLED=0

WORKDIR /usr/src/snowflake-id

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
  go build -o ./snowflake-id ./cmd

FROM scratch
ENV PATH=/bin
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
COPY --from=build /usr/src/snowflake-id/snowflake-id /bin/
USER nobody:nobody
EXPOSE 7669 8001
ENTRYPOINT ["/bin/snowflake-id"]