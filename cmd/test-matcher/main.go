// Command test-matcher runs the configured matcher against a couple of sample
// jobs so you can see scoring quality and a generated cover letter without
// touching the database or a real scraper.
//
//	go run ./cmd/test-matcher -config config.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/matcher"
	"github.com/yalimeiri/job-agent/internal/models"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config YAML")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config:", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	mt, err := matcher.New(cfg.Matching, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build matcher:", err)
		os.Exit(1)
	}

	fmt.Printf("Provider: %s | model: %s\n", cfg.Matching.Provider, cfg.Matching.Model)
	fmt.Printf("Profile : %s | roles: %v | skills: %v\n\n",
		cfg.Profile.FullName, cfg.Profile.TargetRoles, cfg.Profile.Skills)

	jobs := sampleJobs()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for i, job := range jobs {
		fmt.Printf("──────────────────────────────────────────────\n")
		fmt.Printf("JOB %d: %s @ %s (%s)\n", i+1, job.Title, job.Company, job.Location)

		start := time.Now()
		res, err := mt.Match(ctx, job, cfg.Profile)
		if err != nil {
			fmt.Printf("  ERROR: %v\n\n", err)
			continue
		}
		fmt.Printf("  Score    : %.2f   Relevant: %t   (%s)\n", res.Score, res.Relevant, time.Since(start).Round(time.Millisecond))
		fmt.Printf("  Reasoning: %s\n", res.Reasoning)
		if len(res.Strengths) > 0 {
			fmt.Printf("  Strengths: %v\n", res.Strengths)
		}
		if len(res.KeySkillGaps) > 0 {
			fmt.Printf("  Gaps     : %v\n", res.KeySkillGaps)
		}
		if res.CoverLetter != "" {
			fmt.Printf("  Cover letter:\n    %s\n", indent(res.CoverLetter))
		}
		fmt.Println()
	}
}

func sampleJobs() []*models.Job {
	return []*models.Job{
		{
			Title: "Senior Full Stack Developer", Company: "Wix", Location: "Tel Aviv, IL",
			Remote: false, JobType: "FULLTIME",
			Requirements: "React, Node.js, TypeScript, PostgreSQL, 5+ years experience",
			Description:  "Build and scale large web applications across the stack with a modern JS toolchain.",
		},
		{
			Title: "Senior Salesforce Administrator", Company: "SomeCorp", Location: "Haifa, IL",
			Remote: false, JobType: "FULLTIME",
			Requirements: "Salesforce certification, Apex, 5+ years CRM administration",
			Description:  "Administer and customize Salesforce CRM, manage workflows and reports.",
		},
	}
}

func indent(s string) string {
	out := ""
	for _, line := range splitLines(s) {
		out += line + "\n    "
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
