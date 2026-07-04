package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/app"
	"whatsapp-payment-demo/internal/config"
)

func main() {
	if err := run(); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		fmt.Print("Password to hash: ")
		password, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(password)), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		fmt.Println(string(hash))
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	command := "server"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer application.Close()

	switch command {
	case "server":
		return application.RunServer(ctx)
	case "migrate":
		return application.Migrate(ctx)
	case "seed":
		return application.Seed(ctx)
	case "reconcile":
		return application.Reconcile(ctx)
	case "retain":
		return application.PurgeExpiredData(ctx)
	case "sync-vtpass-data-plans":
		return application.SyncVTPassDataPlans(ctx)
	case "health":
		return application.Health(ctx)
	default:
		return fmt.Errorf("unknown command %q; expected server, migrate, seed, reconcile, retain, sync-vtpass-data-plans, health, or hash-password", command)
	}
}
