// Command shipyard is the command-line client for shipyardd.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/config"
)

const (
	commandTimeout = 15 * time.Second
	usageText      = `Shipyard registers applications with a shipyardd API.

Usage:
  shipyard [--api-url URL] apps create --name NAME --repo URL
  shipyard [--api-url URL] apps list
  shipyard [--api-url URL] apps get NAME

The API base URL is taken from --api-url, then SHIPYARD_API_URL,
then http://127.0.0.1:8080.

`
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	fs := flag.NewFlagSet("shipyard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", "", "Shipyard API base URL (overrides "+config.EnvAPIURL+")")
	fs.Usage = func() {
		fprintf(fs.Output(), "%s", usageText)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	if rest[0] != "apps" {
		fprintf(stderr, "unknown command %q\n\n", rest[0])
		fs.Usage()
		return 2
	}

	base := strings.TrimSpace(*apiURL)
	if base == "" {
		base = strings.TrimSpace(getenv(config.EnvAPIURL))
	}
	if base == "" {
		base = config.DefaultAPIURL
	}
	return runApps(rest[1:], newAPIClient(base), stdout, stderr)
}

func runApps(args []string, client *apiClient, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fprintln(stderr, "usage: shipyard apps create|list|get")
		return 2
	}
	switch args[0] {
	case "create":
		return runAppsCreate(args[1:], client, stdout, stderr)
	case "list":
		return runAppsList(args[1:], client, stdout, stderr)
	case "get":
		return runAppsGet(args[1:], client, stdout, stderr)
	default:
		fprintf(stderr, "unknown apps command %q\n", args[0])
		return 2
	}
}

func runAppsCreate(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apps create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "application name")
	repo := fs.String("repo", "", "https GitHub repository URL")
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard apps create --name NAME --repo URL")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || strings.TrimSpace(*name) == "" || strings.TrimSpace(*repo) == "" {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	app, err := client.createApp(ctx, strings.TrimSpace(*name), strings.TrimSpace(*repo))
	if err != nil {
		return writeCLIError(stderr, err)
	}
	return writeJSON(stdout, stderr, app)
}

func runAppsList(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apps list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard apps list")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	apps, err := client.listApps(ctx)
	if err != nil {
		return writeCLIError(stderr, err)
	}
	return writeJSON(stdout, stderr, apps)
}

func runAppsGet(args []string, client *apiClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apps get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(fs.Output(), "usage: shipyard apps get NAME")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	app, err := client.getApp(ctx, fs.Arg(0))
	if err != nil {
		return writeCLIError(stderr, err)
	}
	return writeJSON(stdout, stderr, app)
}

func writeJSON(w io.Writer, stderr io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return writeCLIError(stderr, err)
	}
	return 0
}

func writeCLIError(stderr io.Writer, err error) int {
	fprintf(stderr, "error: %s\n", err)
	return 1
}

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func fprintln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}
