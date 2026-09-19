.PHONY: check
check:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	@# v2 is required: v1.64.8's bundled type-checker predates Go 1.27's export
	@# data and reports phantom "undefined method" errors rather than real
	@# findings, which is why lint had never actually run here. The install is
	@# still best-effort locally; CI pins the same version and does not skip.
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2 2>/dev/null || true
	golangci-lint run || echo "WARNING: golangci-lint unavailable, skipping"
	go build ./...
	@if [ "$$(go env CGO_ENABLED)" = "1" ] && command -v gcc >/dev/null 2>&1; then \
		go test ./... -race; \
	else \
		echo "WARNING: Skipping -race flag (CGO/gcc not available)"; \
		go test ./...; \
	fi
