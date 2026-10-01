package main

import (
	"context"
	"errors"
	"instore-shopper/internal/shop"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	addr := env("APP_ADDR", "127.0.0.1:8090")
	origin := env("APP_ORIGIN", "http://127.0.0.1:8090")

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		u, err := url.Parse(origin)
		if err != nil {
			log.Fatal(err)
		}
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			log.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
		if err != nil {
			log.Fatal(err)
		}
		request.Host = u.Host
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			log.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			log.Fatalf("healthcheck returned %d", response.StatusCode)
		}
		return
	}
	path := env("DATABASE_PATH", "var/shop.db")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		log.Fatal(e)
	}
	s, e := shop.Open(path)
	if e != nil {
		log.Fatal(e)
	}
	defer s.Close()
	u, e := url.Parse(origin)
	if e != nil {
		log.Fatalf("invalid APP_ORIGIN: %v", e)
	}
	app, e := shop.New(s, shop.Config{Origin: origin, ManagerPassword: os.Getenv("MANAGER_PASSWORD"), SecureCookies: u.Scheme == "https", DemoMode: os.Getenv("DEMO_MODE") == "true"})
	if e != nil {
		log.Fatal(e)
	}
	server := &http.Server{Addr: addr, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := server.Shutdown(shutdown); e != nil {
			log.Printf("shutdown: %v", e)
		}
	}()
	log.Printf("Neighborhood Market demo at %s (listening %s)", origin, addr)
	if os.Getenv("DEMO_MODE") == "true" {
		log.Print("Shared demo mode enabled: use fake data only; visitors may be able to change demo inventory and orders")
	}
	if os.Getenv("MANAGER_PASSWORD") == "" {
		log.Print("Manager access disabled: set MANAGER_PASSWORD to enable it")
	}
	if e = server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal(e)
	}
}
