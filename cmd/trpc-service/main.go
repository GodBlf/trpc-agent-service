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
	servicelog "github.com/liuzengh/trpc-agent-service/trpcservice/log"
	"github.com/liuzengh/trpc-agent-service/trpcservice/platform"
	"github.com/liuzengh/trpc-agent-service/trpcservice/web"
)

func main() {
	redactor := servicelog.NewRedactor([]string{
		os.Getenv("TRPC_AUTH_HMAC_SECRET"), os.Getenv("TRPC_TELEGRAM_BOT_TOKEN"), os.Getenv("TRPC_WECOM_BOT_SECRET"),
		os.Getenv("TRPC_REDIS_ADDR"), os.Getenv("TRPC_MIGRATION_REDIS_ADDR"), os.Getenv("TRPC_BACKEND_SELECTIONS"),
	}, nil)
	log.SetOutput(servicelog.NewRedactingWriter(os.Stderr, redactor))
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
	governancePath := os.Getenv("TRPC_GOVERNANCE_PATH")
	if governancePath == "" {
		governancePath = "data/governance.json"
	}
	governance, err := platform.NewPersistentGovernanceCenter(governancePath)
	if err != nil {
		log.Fatalf("governance state: %v", err)
	}
	admin.ConfigureGovernance(governance)
	switch authMode := os.Getenv("TRPC_AUTH_MODE"); authMode {
	case "", "development":
	case "production":
		directoryPath := os.Getenv("TRPC_IDENTITY_DIRECTORY")
		directory, err := platform.LoadIdentityDirectory(directoryPath)
		if err != nil {
			log.Fatalf("identity directory: %v", err)
		}
		issuer, audience, secret := os.Getenv("TRPC_AUTH_ISSUER"), os.Getenv("TRPC_AUTH_AUDIENCE"), os.Getenv("TRPC_AUTH_HMAC_SECRET")
		if issuer == "" || audience == "" || secret == "" {
			log.Fatal("production identity requires issuer, audience, and signing secret")
		}
		admin.ConfigureIdentityProvider(platform.NewJWTIdentityProvider(platform.JWTIdentityConfig{Issuer: issuer, Audience: audience, HMACSecret: []byte(secret)}, directory))
	default:
		log.Fatalf("unsupported authentication mode %q", authMode)
	}
	if err := admin.ConfigureBackendSelections(os.Getenv("TRPC_BACKEND_SELECTIONS")); err != nil && os.Getenv("TRPC_BACKEND_SELECTIONS") != "" {
		log.Fatalf("backend selections: %v", err)
	}
	admin.ConfigureRuntime(platform.NewFrameworkRunnerAdapter(store.DeploymentVersion, nil), life)
	admin.ConfigureBackendCatalog(os.Getenv("TRPC_REDIS_ADDR"), os.Getenv("TRPC_SQLITE_PATH"))
	admin.ConfigureMigration(os.Getenv("TRPC_MIGRATION_REDIS_ADDR"), os.Getenv("TRPC_MIGRATION_SQLITE_PATH"), os.Getenv("TRPC_MIGRATION_CHECKPOINT_PATH"))
	routePath := os.Getenv("TRPC_BOT_ROUTES_PATH")
	if routePath == "" {
		routePath = "data/bot-routes.json"
	}
	routes, err := platform.NewPersistentBotTenantAllowlist(routePath)
	if err != nil {
		log.Fatalf("bot tenant allowlist: %v", err)
	}
	providers := platform.NewProviderRuntime(platform.LoadBotConfig(nil), routes, admin.ProcessProviderMessage)
	admin.ConfigureProviderRuntime(providers)
	providers.Start(context.Background())
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
