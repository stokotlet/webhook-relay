FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/receiver ./cmd/receiver

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 relay
COPY --from=build /out/relay /out/receiver /usr/local/bin/
USER relay
EXPOSE 8080 9090
ENTRYPOINT ["relay"]
