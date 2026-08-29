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
	"github.com/liuzengh/trpc-agent-service/trpcservice/lifecycle"
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

	life := lifecycle.New()
	store := platform.NewMemoryPlatform()
	identity := platform.DevelopmentIdentity{
		ID: "local-developer", Name: "Local Developer",
		Assignments: []platform.TenantAssignment{
			{TenantID: "tenant-dev", TenantName: "Development Tenant", Role: platform.RolePlatformAdmin},
			{TenantID: "tenant-view", TenantName: "Read-only Tenant", Role: platform.RoleViewer},
		},
	}
	admin := platform.NewAdminHandler(store, identity)
	if err := admin.ConfigureBackendSelections(os.Getenv("TRPC_BACKEND_SELECTIONS")); err != nil && os.Getenv("TRPC_BACKEND_SELECTIONS") != "" {
		log.Fatalf("backend selections: %v", err)
	}
	admin.ConfigureRuntime(platform.EchoRunner{}, life)
	admin.ConfigureBackendCatalog(os.Getenv("TRPC_REDIS_ADDR"), os.Getenv("TRPC_SQLITE_PATH"))
	admin.ConfigureMigration(os.Getenv("TRPC_MIGRATION_REDIS_ADDR"), os.Getenv("TRPC_MIGRATION_SQLITE_PATH"), os.Getenv("TRPC_MIGRATION_CHECKPOINT_PATH"))
	server := &http.Server{Addr: *addr, Handler: web.NewStage1Handler(platform.EchoRunner{}, platform.TenantContext{TenantID: "baseline"}, life, admin)}
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
	if err := life.Shutdown(ctx); err != nil {
		log.Printf("lifecycle shutdown: %v", err)
	}
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	if err := admin.Close(); err != nil {
		log.Printf("data stores: %v", err)
	}
}
