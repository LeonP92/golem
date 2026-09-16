package main

import "fmt"

// syncPlan is what startup decided to do about the GitHub sync worker, and
// the one line it will tell the operator about that decision.
type syncPlan struct {
	start bool
	log   string
}

// githubSyncPlan decides whether to construct and start ghsync.Worker.
//
// The only thing that gates starting is the token, NOT the number of enabled
// repositories. That is deliberate and is the fix for a cold-start defect:
// this decision is made once, at startup, and nothing re-runs it, so keying
// it on a repo count meant a fresh install — which by definition has zero
// enabled repositories — never built a worker, and enabling the first
// repository through /settings/github did nothing at all until someone
// restarted the process. Nothing said so: the settings page advertises a
// 15-minute poll, Sync now answered 503, and last_polled_at stayed NULL
// forever.
//
// Starting with zero repositories is close to free and entirely safe.
// Worker.ingestAll re-queries `enabled = true` on every pass, so an empty
// result is one indexed SELECT per poll interval; Worker.ingestOne resolves
// the repo by ID when a manual trigger fires, so a repository enabled after
// startup is a first-class case rather than a race. The drain loop likewise
// walks an empty outbox.
//
// An install with no token still constructs no worker, so main's Sync field
// stays a nil interface and manualSync answers 503 rather than panicking —
// see the typed-nil note at that assignment. That is the "no GitHub at all"
// deployment, and it must keep starting clean and staying off.
//
// tokenEnv is the variable's NAME; token is its value, and is never logged.
func githubSyncPlan(tokenEnv, token string, enabledRepos int64) syncPlan {
	if token == "" {
		if enabledRepos == 0 {
			return syncPlan{
				start: false,
				log: fmt.Sprintf("github sync: %s is empty and no repositories are enabled — "+
					"sync idle (set the token and restart to enable GitHub Issues)", tokenEnv),
			}
		}
		return syncPlan{
			start: false,
			log: fmt.Sprintf("ERROR github sync: %d repo(s) enabled but %s is empty — "+
				"sync DISABLED until a token is provided and the orchestrator is restarted",
				enabledRepos, tokenEnv),
		}
	}
	if enabledRepos == 0 {
		return syncPlan{
			start: true,
			log: "github sync: starting with no repositories enabled — enable one at " +
				"/settings/github and it takes effect on the next poll, or immediately via Sync now",
		}
	}
	return syncPlan{
		start: true,
		log:   fmt.Sprintf("github sync: %d repo(s) enabled", enabledRepos),
	}
}
