FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /edr .

FROM debian:bookworm-slim
COPY --from=builder /edr /usr/local/bin/edr
ENTRYPOINT ["edr"]
