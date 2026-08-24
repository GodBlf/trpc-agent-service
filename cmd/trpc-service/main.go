package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
	"github.com/liuzengh/trpc-agent-service/trpcservice/platform"
	"github.com/liuzengh/trpc-agent-service/trpcservice/web"
)

func main() {
	defaultAddr := os.Getenv("TRPC_SERVICE_ADDR")
	if defaultAddr == "" {
		defaultAddr = ":8080"
	}
	addr := flag.String("addr", defaultAddr, "HTTP listen address")
	flag.Parse()

	server := &http.Server{Addr: *addr, Handler: web.NewHandlerWithRunner(platform.EchoRunner{}, platform.TenantContext{TenantID: "baseline"})}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		fmt.Printf("trpc-agent-service %s listening on %s\n", trpcservice.Version, *addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server: %v", err)
		}
	}()

	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
}
