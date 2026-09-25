FROM golang:1.23-alpine AS build
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod *.go ./
COPY config/*.example.json ./config/
RUN go vet ./... && go test ./...
RUN go build -trimpath -ldflags='-s -w' -o /sorter .
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /sorter /sorter
USER 65532:65532
ENTRYPOINT ["/sorter"]
