// Command shipyardd serves the Shipyard HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/api"
	"github.com/gabbygtobar-coder/shipyard/internal/config"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	cfg := config.Load()

	fs := flag.NewFlagSet("shipyardd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", cfg.Addr, "listen address (env "+config.EnvAddr+")")
	fs.Usage = func() {
		fprintf(fs.Output(), "Usage: shipyardd [--addr %s]\n", config.DefaultAddr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fprintln(stderr, "unexpected arguments")
		fs.Usage()
		return 2
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewHandler(store.NewMemory(), logger),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", *addr)
		errCh <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			return 1
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "err", err)
			return 1
		}
	}
	return 0
}

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func fprintln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}
