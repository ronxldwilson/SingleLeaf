FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o single-leaf .

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/single-leaf /usr/local/bin/single-leaf
EXPOSE 8081
ENTRYPOINT ["single-leaf"]
