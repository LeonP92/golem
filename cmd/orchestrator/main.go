package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/config"
	"github.com/leonp92/golem/internal/orchestrator/db"
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
	//   orchestrator users add <username>
	//   orchestrator users remove <username>
	//   orchestrator shems add --name <name>
	//   orchestrator shems remove <name>
	if len(args) >= 2 && (args[0] == "users" || args[0] == "shems") {
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
					log.Fatal("usage: orchestrator users add <username>")
				}
				if err := admin.UsersAdd(gdb, args[2]); err != nil {
					log.Fatalf("users add: %v", err)
				}
				fmt.Printf("User %q added.\n", args[2])
			case "remove":
				if len(args) < 3 {
					log.Fatal("usage: orchestrator users remove <username>")
				}
				if err := admin.UsersRemove(gdb, args[2]); err != nil {
					log.Fatalf("users remove: %v", err)
				}
				fmt.Printf("User %q removed.\n", args[2])
			default:
				log.Fatalf("unknown users subcommand %q; expected add|remove", args[1])
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
		if err := admin.UsersAddOrUpdate(gdb, username, password); err != nil {
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
	srv := server.New(gdb, hub, broker)
	addr := fmt.Sprintf(":%d", cfg.Port)
	if cfg.TLS.Cert != "" && cfg.TLS.Key != "" {
		log.Printf("listening on %s (TLS)", addr)
		log.Fatal(http.ListenAndServeTLS(addr, cfg.TLS.Cert, cfg.TLS.Key, srv.Routes()))
	} else {
		log.Printf("listening on %s", addr)
		log.Fatal(http.ListenAndServe(addr, srv.Routes()))
	}
}
