// Command test-scraper is a small CLI to verify a JSearch RapidAPI key and see
// what jobs come back for a query, without touching the database.
//
//	go run ./cmd/test-scraper -key "$RAPIDAPI_KEY" -query "Full Stack Developer in Israel"
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
	"github.com/yalimeiri/job-agent/internal/scraper"
)

func main() {
	key := flag.String("key", os.Getenv("RAPIDAPI_KEY"), "RapidAPI key (defaults to $RAPIDAPI_KEY)")
	query := flag.String("query", "Full Stack Developer in Israel", "search query (pipe-separated for multiple)")
	datePosted := flag.String("date", "3days", "date_posted: today|3days|week|month")
	remote := flag.Bool("remote", false, "remote jobs only")
	baseURL := flag.String("url", "", "override JSearch base URL (for a mock or self-hosted proxy)")
	flag.Parse()

	if *key == "" {
		fmt.Fprintln(os.Stderr, "error: a RapidAPI key is required (-key or $RAPIDAPI_KEY)")
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	scr, err := scraper.NewRegistry().Build(config.SourceConfig{
		Name: "jsearch_cli", Type: "jsearch", Enabled: true,
		Extra: map[string]string{
			"api_key":     *key,
			"queries":     *query,
			"date_posted": *datePosted,
			"remote_only": fmt.Sprintf("%t", *remote),
			"base_url":    *baseURL,
		},
	}, scraper.Deps{Limiters: ratelimit.NewRegistry(), Logger: log})
	if err != nil {
		fmt.Fprintln(os.Stderr, "build scraper:", err)
		os.Exit(1)
	}

	start := time.Now()
	res, err := scr.Scrape(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scrape error:", err)
		// keep going to print whatever was collected
	}

	fmt.Printf("\nFound %d jobs in %s:\n\n", len(res.Jobs), time.Since(start).Round(time.Millisecond))
	for i, j := range res.Jobs {
		fmt.Printf("%2d. %s — %s\n    %s | remote=%t | %s\n    %s\n\n",
			i+1, j.Title, j.Company, j.Location, j.Remote, j.Salary, j.ApplyURL)
	}
	if len(res.Jobs) == 0 {
		os.Exit(1)
	}
}
