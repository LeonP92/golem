package worker

import (
	"fmt"

	"github.com/leonp92/golem/internal/shem/client"
)

// PostCheckpointWithRetry uses the client's built-in exponential-backoff retry
// logic with a per-call retry budget. The copy shares the HTTP transport without
// changing the retry settings used by concurrent requests on the original client.
func PostCheckpointWithRetry(c *client.Client, ticketID string, phase, sha string, maxAttempts int) error {
	checkpointClient := *c
	checkpointClient.RetryAttempts = maxAttempts

	if err := checkpointClient.PostCheckpoint(ticketID, phase, sha); err != nil {
		return fmt.Errorf("checkpoint exhausted retries: %w", err)
	}
	return nil
}
