package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/config"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
	"github.com/leonp92/golem/internal/orchestrator/server"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func main() {
	args := os.Args[1:]

	// Admin subcommands operate directly on the database and exit without
	// starting the HTTP server.
	//
	// Usage:
	//   orchestrator users add <username> [--role admin|developer]
	//   orchestrator users list
	//   orchestrator users set-role <username> <role>
	//   orchestrator users remove <username>
	//   orchestrator shems add --name <name>
	//   orchestrator shems remove <name>
	//   orchestrator backfill body-hash [--dry-run]
	if len(args) >= 2 && (args[0] == "users" || args[0] == "shems" || args[0] == "backfill") {
		dsn := os.Getenv("ORCHESTRATOR_DB")
		if dsn == "" {
			dsn = "orchestrator.db"
		}
		gdb, err := db.Open(dsn)
		if err != nil {
			log.Fatalf("db: %v", err)
		}

		switch args[0] {
		case "users":
			switch args[1] {
			case "add":
				if len(args) < 3 {
					log.Fatal("usage: orchestrator users add <username> [--role admin|developer]")
				}
				role := string(rbac.RoleDeveloper)
				for i := 3; i < len(args)-1; i++ {
					if args[i] == "--role" {
						role = args[i+1]
						break
					}
				}
				if err := admin.UsersAdd(gdb, args[2], role); err != nil {
					log.Fatalf("users add: %v", err)
				}
				fmt.Printf("User %q added with role %q.\n", args[2], role)
			case "list":
				users, err := admin.UsersList(gdb)
				if err != nil {
					log.Fatalf("users list: %v", err)
				}
				for _, u := range users {
					fmt.Printf("%s\t%s\n", u.Username, u.Role)
				}
			case "set-role":
				if len(args) < 4 {
					log.Fatal("usage: orchestrator users set-role <username> <role>")
				}
				if err := admin.UsersSetRole(gdb, args[2], args[3]); err != nil {
					log.Fatalf("users set-role: %v", err)
				}
				fmt.Printf("User %q role set to %q.\n", args[2], args[3])
			case "remove":
				if len(args) < 3 {
					log.Fatal("usage: orchestrator users remove <username>")
				}
				if err := admin.UsersRemove(gdb, args[2]); err != nil {
					log.Fatalf("users remove: %v", err)
				}
				fmt.Printf("User %q removed.\n", args[2])
			default:
				log.Fatalf("unknown users subcommand %q; expected add|list|set-role|remove", args[1])
			}
		case "shems":
			switch args[1] {
			case "add":
				name := ""
				for i := 2; i < len(args)-1; i++ {
					if args[i] == "--name" {
						name = args[i+1]
						break
					}
				}
				if name == "" {
					log.Fatal("usage: orchestrator shems add --name <name>")
				}
				if err := admin.ShemsAdd(gdb, name); err != nil {
					log.Fatalf("shems add: %v", err)
				}
			case "remove":
				if len(args) < 3 {
					log.Fatal("usage: orchestrator shems remove <name>")
				}
				if err := admin.ShemsRemove(gdb, args[2]); err != nil {
					log.Fatalf("shems remove: %v", err)
				}
				fmt.Printf("Shem %q removed.\n", args[2])
			default:
				log.Fatalf("unknown shems subcommand %q; expected add|remove", args[1])
			}
		case "backfill":
			// One-shot repair for databases written by a deployment made
			// partway through the GitHub Issues work, where issue_number had
			// shipped but body_hash had not. See admin.BackfillBodyHash for
			// exactly which rows qualify and why nothing else fixes them.
			if args[1] != "body-hash" {
				log.Fatalf("unknown backfill subcommand %q; expected body-hash", args[1])
			}
			dryRun := false
			for _, a := range args[2:] {
				switch a {
				case "--dry-run":
					dryRun = true
				default:
					log.Fatalf("usage: orchestrator backfill body-hash [--dry-run]")
				}
			}
			res, err := admin.BackfillBodyHash(gdb, dryRun)
			if err != nil {
				log.Fatalf("backfill body-hash: %v", err)
			}
			fmt.Println(res)
		}
		return
	}

	// Normal server mode: first argument (if present and not a subcommand) is
	// treated as the config file path.
	cfgPath := "orchestrator.yaml"
	if len(args) > 0 {
		cfgPath = args[0]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	gdb, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	// Auto-provision admin user and shem API key from environment variables.
	// Set GOLEM_ADMIN_PASSWORD (and optionally GOLEM_ADMIN_USERNAME) to create
	// or update the admin account on every startup. Set GOLEM_SHEM_API_KEY
	// (and optionally GOLEM_SHEM_NAME) to register a shem without the manual
	// "shems add" step.
	if password := os.Getenv("GOLEM_ADMIN_PASSWORD"); password != "" {
		username := os.Getenv("GOLEM_ADMIN_USERNAME")
		if username == "" {
			username = "admin"
		}
		if err := admin.UsersAddOrUpdate(gdb, username, password, string(rbac.RoleAdmin)); err != nil {
			log.Printf("warn: auto-provision admin user %q: %v", username, err)
		} else {
			log.Printf("auto-provisioned admin user %q", username)
		}
	}
	if key := os.Getenv("GOLEM_SHEM_API_KEY"); key != "" {
		name := os.Getenv("GOLEM_SHEM_NAME")
		if name == "" {
			name = "default"
		}
		if err := admin.ShemsAddOrUpdate(gdb, name, key); err != nil {
			log.Printf("warn: auto-provision shem %q: %v", name, err)
		} else {
			log.Printf("auto-provisioned shem %q", name)
		}
	}
	hub := ws.NewHub()
	broker := sse.NewBroker()
	ws.StartHeartbeatMonitor(context.Background(), gdb, hub, 60*time.Second, 90*time.Second)

	// GitHub sync: started whenever a token is present, whether or not any
	// repository is enabled yet — see githubSyncPlan for why the repo count
	// must not gate this. Required secrets are validated at startup with a
	// loud log message, not a silent no-op, and a missing token never
	// prevents the rest of the orchestrator from serving.
	var ghWorker *ghsync.Worker
	token := os.Getenv(cfg.GitHub.TokenEnv)
	var enabledRepos int64
	if err := gdb.Model(&db.GitHubRepo{}).Where("enabled = ?", true).Count(&enabledRepos).Error; err != nil {
		// The count is informational — it only chooses which message to
		// print — so a failure here must not decide the token question. It
		// is reported and then treated as zero.
		log.Printf("ERROR github sync: count enabled repos: %v — continuing as if none were enabled", err)
		enabledRepos = 0
	}
	plan := githubSyncPlan(cfg.GitHub.TokenEnv, token, enabledRepos)
	log.Print(plan.log)
	if plan.start {
		if cfg.BaseURL == "" {
			// Not fatal — sync still runs — but every milestone comment
			// ghsync posts to a real GitHub issue would otherwise end in a
			// bare "/tickets/<id>" with no host, a silently broken link.
			log.Printf("WARNING github sync: base_url is empty — milestone " +
				"comments will link to a relative /tickets/<id> path; set " +
				"base_url in the config to the orchestrator's externally " +
				"reachable URL")
		}
		client, err := github.New(token, cfg.GitHub.APIBase)
		if err != nil {
			log.Printf("ERROR github sync: client init failed, sync DISABLED: %v", err)
		} else {
			ghWorker = ghsync.NewWorker(
				ghsync.NewSyncer(gdb, client),
				cfg.GitHub.PollIntervalDuration(),
				cfg.GitHub.DrainIntervalDuration(),
			)
			ghWorker.Start(context.Background())
			defer ghWorker.Stop()
			log.Printf("github sync: started (ingest %v, drain %v)",
				cfg.GitHub.PollIntervalDuration(), cfg.GitHub.DrainIntervalDuration())
		}
	}

	secureCookie := cfg.TLS.Cert != "" && cfg.TLS.Key != ""
	srv := server.New(gdb, hub, broker, secureCookie, cfg.BaseURL)
	srv.ManualSyncCooldown = cfg.GitHub.ManualSyncCooldownDuration()
	srv.CSPMode = cfg.CSP.Mode
	srv.GitHubTokenEnv = cfg.GitHub.TokenEnv
	// Only assign Sync when the worker was actually started. ghWorker is a
	// *ghsync.Worker; assigning a nil *ghsync.Worker to the api.SyncTrigger
	// interface field would produce a non-nil interface holding a nil
	// pointer, so h.Sync == nil in the handler would be false and the first
	// call into it would panic instead of returning 503. Guarding here keeps
	// the interface itself nil whenever sync isn't running.
	if ghWorker != nil {
		srv.Sync = ghWorker
	}
	addr := fmt.Sprintf(":%d", cfg.Port)
	if secureCookie {
		log.Printf("listening on %s (TLS)", addr)
		log.Fatal(http.ListenAndServeTLS(addr, cfg.TLS.Cert, cfg.TLS.Key, srv.Routes()))
	} else {
		log.Printf("listening on %s", addr)
		log.Fatal(http.ListenAndServe(addr, srv.Routes()))
	}
}
