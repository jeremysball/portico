FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/portico ./cmd/portico

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/portico /usr/local/bin/portico
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["portico"]
