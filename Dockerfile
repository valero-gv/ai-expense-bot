FROM golang:1.26.9-alpine AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM deps AS test
RUN apk add --no-cache gcc musl-dev
COPY . .
CMD ["sh", "-c", "go vet ./... && go test -race -count=1 ./..."]

FROM deps AS build
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/expensebot ./cmd/server

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/expensebot /expensebot
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/expensebot"]
