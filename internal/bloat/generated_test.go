package bloat

import "testing"

func TestIsGenerated(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"services/queue-bff/pacts/queue-bff-queue-svc-account-tag.json", true},
		{"pacts/contract.json", true},
		{"go.sum", true},
		{"frontends/queue-mfe/package-lock.json", true},
		{"pnpm-lock.yaml", true},
		{"yarn.lock", true},
		{"Cargo.lock", true},
		{"services/queue-svc/uv.lock", true},
		{"services/queue-svc/poetry.lock", true},
		{"src/components/__snapshots__/TagChip.test.tsx.snap", true},
		{"gen/queue/v1/account.pb.go", true},
		{"gen/queue/v1/account_grpc.pb.go", true},
		{"queue_svc/gen/account_pb2.py", true},
		{"queue_svc/gen/account_pb2.pyi", true},
		{"queue_svc/gen/account_pb2_grpc.py", true},

		{"services/queue-bff/internal/pact/consumer/account_tag_consumer_test.go", false},
		{"go.mod", false},
		{"package.json", false},
		{"pacts/README.md", false},
		{"src/pacts.json", false},
		{"internal/lock/lock.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsGenerated(tt.path); got != tt.want {
				t.Errorf("IsGenerated(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
