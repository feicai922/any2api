package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"api-gateway/internal/config"
	"api-gateway/internal/gateway"
	"api-gateway/internal/panel"
)

func main() {
	configPath := flag.String("config", "config.json", "path to gateway config JSON")
	adminListen := flag.String("admin-listen", "127.0.0.1:7862", "local-only management panel listen address")
	startAll := flag.Bool("start-all", true, "start configured upstream projects with the gateway")
	flag.Parse()

	if err := applyUnifiedCredentials(); err != nil {
		log.Fatalf("credentials: %v", err)
	}

	adminAddr, err := net.ResolveTCPAddr("tcp", *adminListen)
	if err != nil {
		log.Fatalf("admin listen address: %v", err)
	}
	if !adminAddr.IP.IsLoopback() {
		log.Fatalf("admin panel must listen on a loopback address, got %q", *adminListen)
	}
	if adminAddr.Port == 0 {
		log.Fatalf("admin panel port must not be zero")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	resolved, err := config.Resolve(cfg)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	g, err := gateway.New(resolved, cfg.DefaultProject, cfg.MaxBodyBytes, time.Duration(cfg.UpstreamTimeout)*time.Second, os.Getenv("GATEWAY_API_KEY"))
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}
	root, err := filepath.Abs(filepath.Dir(*configPath))
	if err != nil {
		log.Fatalf("workspace: %v", err)
	}
	ph, err := panel.New(root, cfg.Projects, true)
	if err != nil {
		log.Fatalf("panel: %v", err)
	}
	if *startAll {
		results := ph.StartAll(context.Background())
		for _, result := range results {
			log.Printf("project %s: %s (%s)", result.Project, result.Action, result.Message)
		}
	}

	publicHandler := panelWithSecurity(ph.PublicHandler(g.Handler()))
	adminHandler := panelWithSecurity(ph.AdminHandler())
	publicSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           publicHandler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	adminSrv := &http.Server{
		Addr:              *adminListen,
		Handler:           adminHandler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("api-gateway listening on http://%s", cfg.Listen)
		if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("public listen: %v", err)
		}
	}()
	go func() {
		log.Printf("local panel listening on http://%s", *adminListen)
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("admin listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := publicSrv.Shutdown(ctx); err != nil {
		log.Printf("public shutdown: %v", err)
	}
	if err := adminSrv.Shutdown(ctx); err != nil {
		log.Printf("admin shutdown: %v", err)
	}
	fmt.Println("api-gateway stopped")
}

func applyUnifiedCredentials() error {
	secret := os.Getenv("API_GATEWAY_ADMIN_PASSWORD")
	if strings.TrimSpace(secret) == "" {
		secret = os.Getenv("PANEL_PASSWORD")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("API_GATEWAY_ADMIN_PASSWORD or PANEL_PASSWORD is required")
	}
	for _, name := range []string{
		"API_GATEWAY_ADMIN_PASSWORD",
		"PANEL_PASSWORD",
		"GATEWAY_API_KEY",
		"WORKBUDDY_API_KEY",
		"QODER_API_KEY",
		"OPENCODE_API_KEY",
		"TRAE_API_KEY",
		"GCLI_API_KEY",
		"WB2A_API_KEY",
		"QODER2API_CONSOLE_PASSWORD",
		"API_KEY",
		"RELAY_API_KEYS",
		"API_PASSWORD",
		"PASSWORD",
	} {
		if err := os.Setenv(name, secret); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
	}
	return nil
}

func panelWithSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}
