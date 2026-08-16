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
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/app"
	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/logging"
	"whatsapp-payment-demo/internal/totp"
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
	if len(os.Args) > 1 && os.Args[1] == "random-totp-key" {
		key, err := totp.RandomKeyHex()
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	}
	if len(os.Args) > 1 && os.Args[1] == "random-data-key" {
		key, err := totp.RandomKeyHex()
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(cfg.LogLevel, cfg.LogFormat, os.Stdout)
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
	case "reconcile3":
		return application.ReconcileThreeWay(ctx)
	case "settle":
		if len(os.Args) < 4 {
			return fmt.Errorf("usage: settle <merchant-id> <batch-no>")
		}
		merchantID, err := uuid.Parse(os.Args[2])
		if err != nil {
			return fmt.Errorf("invalid merchant id: %w", err)
		}
		return application.Settle(ctx, merchantID, os.Args[3])
	case "retain":
		return application.PurgeExpiredData(ctx)
	case "rescreen":
		return application.RescreenDue(ctx)
	case "recompute-risk":
		return application.RecomputeAllRisk(ctx)
	case "monitor":
		return application.MonitorTransactions(ctx)
	case "reports":
		dir := "reports/exports"
		if len(os.Args) > 2 {
			dir = os.Args[2]
		}
		paths, err := application.ExportReportsCSV(ctx, dir, 24*30*time.Hour)
		if err != nil {
			return err
		}
		fmt.Println("wrote", len(paths), "report(s):")
		for _, p := range paths {
			fmt.Println("  " + p)
		}
		return nil
	case "sync-vtpass-data-plans":
		return application.SyncVTPassDataPlans(ctx)
	case "health":
		return application.Health(ctx)
	default:
		return fmt.Errorf("unknown command %q; expected server, migrate, seed, reconcile, reconcile3, settle, retain, rescreen, recompute-risk, monitor, reports, sync-vtpass-data-plans, health, hash-password, random-totp-key, or random-data-key", command)
	}
}
