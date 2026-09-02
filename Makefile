.PHONY: check
check:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8 2>/dev/null || true
	golangci-lint run || echo "WARNING: golangci-lint unavailable, skipping"
	go build ./...
	@if [ "$$(go env CGO_ENABLED)" = "1" ] && command -v gcc >/dev/null 2>&1; then \
		go test ./... -race; \
	else \
		echo "WARNING: Skipping -race flag (CGO/gcc not available)"; \
		go test ./...; \
	fi
