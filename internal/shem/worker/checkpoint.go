package worker

import (
	"fmt"

	"github.com/leonp92/golem/internal/shem/client"
)

// PostCheckpointWithRetry calls client.PostCheckpoint with up to maxAttempts
// total attempts (using the client's built-in exponential-backoff retry logic).
// It temporarily overrides the client's RetryAttempts field, restoring it on
// return so the client remains usable for other calls.
func PostCheckpointWithRetry(c *client.Client, ticketID string, phase, sha string, maxAttempts int) error {
	orig := c.RetryAttempts
	c.RetryAttempts = maxAttempts
	defer func() { c.RetryAttempts = orig }()

	if err := c.PostCheckpoint(ticketID, phase, sha); err != nil {
		return fmt.Errorf("checkpoint exhausted retries: %w", err)
	}
	return nil
}
