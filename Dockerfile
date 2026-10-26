# fwdzbx: Forward <-> Zabbix integration. Build: docker build -t fwdzbx .
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/fwdzbx ./cmd/fwdzbx

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fwdzbx /usr/local/bin/fwdzbx
EXPOSE 8020
ENTRYPOINT ["/usr/local/bin/fwdzbx"]
CMD ["--help"]
