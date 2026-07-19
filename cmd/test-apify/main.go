// Command test-apify runs a configured Apify Actor and prints the jobs it
// maps, without touching the database. Use it to verify an actor + input JSON
// before wiring the source into config.yaml.
//
//	go run ./cmd/test-apify -token "$APIFY_TOKEN" -actor bebity/linkedin-jobs-scraper \
//	  -input '{"title":"Junior Software Engineer","location":"Israel","rows":25}'
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
	"github.com/yalimeiri/job-agent/internal/scraper"
)

func main() {
	token := flag.String("token", os.Getenv("APIFY_TOKEN"), "Apify API token (defaults to $APIFY_TOKEN)")
	actor := flag.String("actor", "", "Apify actor id, e.g. bebity/linkedin-jobs-scraper")
	input := flag.String("input", "{}", "actor input as a JSON object")
	timeoutSecs := flag.Int("timeout", 180, "actor run timeout in seconds")
	fieldTitle := flag.String("field-title", "", "override: JSON key for the job title")
	fieldCompany := flag.String("field-company", "", "override: JSON key for the company name")
	fieldApplyURL := flag.String("field-apply-url", "", "override: JSON key for the apply URL")
	flag.Parse()

	if *token == "" {
		fmt.Fprintln(os.Stderr, "error: an Apify token is required (-token or $APIFY_TOKEN)")
		os.Exit(2)
	}
	if *actor == "" {
		fmt.Fprintln(os.Stderr, "error: -actor is required, e.g. bebity/linkedin-jobs-scraper")
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	extra := map[string]string{
		"api_token":    *token,
		"actor_id":     *actor,
		"input_json":   *input,
		"timeout_secs": fmt.Sprintf("%d", *timeoutSecs),
	}
	if *fieldTitle != "" {
		extra["field_title"] = *fieldTitle
	}
	if *fieldCompany != "" {
		extra["field_company"] = *fieldCompany
	}
	if *fieldApplyURL != "" {
		extra["field_apply_url"] = *fieldApplyURL
	}

	scr, err := scraper.NewRegistry().Build(config.SourceConfig{
		Name: "apify_cli", Type: "apify", Enabled: true, Extra: extra,
	}, scraper.Deps{Logger: log})
	if err != nil {
		fmt.Fprintln(os.Stderr, "build scraper:", err)
		os.Exit(1)
	}

	fmt.Printf("Running actor %q (timeout %ds)...\n", *actor, *timeoutSecs)
	start := time.Now()
	res, err := scr.Scrape(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scrape error:", err)
	}

	fmt.Printf("\nFound %d jobs in %s:\n\n", len(res.Jobs), time.Since(start).Round(time.Second))
	for i, j := range res.Jobs {
		fmt.Printf("%2d. %s — %s\n    %s | remote=%t | type=%s\n    apply: %s\n\n",
			i+1, j.Title, j.Company, j.Location, j.Remote, j.JobType, j.ApplyURL)
	}
	if len(res.Jobs) == 0 {
		os.Exit(1)
	}
}
