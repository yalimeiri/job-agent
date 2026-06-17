package matcher

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
)

// scoreResponse is the JSON schema every matcher instructs the model to return.
type scoreResponse struct {
	Score        float64  `json:"score"`
	Relevant     bool     `json:"relevant"`
	Reasoning    string   `json:"reasoning"`
	KeySkillGaps []string `json:"key_skill_gaps"`
	Strengths    []string `json:"strengths"`
}

// buildScorePrompt returns the system and user prompts for job scoring. Shared
// across matcher backends so they behave identically regardless of provider.
func buildScorePrompt(job *models.Job, profile config.ProfileConfig) (system, user string) {
	system = "You are a meticulous technical recruiter. Evaluate how well a candidate " +
		"fits a job. Respond with ONLY a single JSON object, no markdown, no code fences. " +
		"Schema: {\"score\": number 0.0-1.0, \"relevant\": boolean, \"reasoning\": string, " +
		"\"key_skill_gaps\": string[], \"strengths\": string[]}. " +
		"score reflects overall fit considering role match, skills, seniority and location."

	user = fmt.Sprintf(`CANDIDATE PROFILE
Name: %s
Years of experience: %d
Target roles: %s
Skills: %s
Summary: %s

JOB
Title: %s
Company: %s
Location: %s (remote=%t)
Type: %s
Requirements: %s
Description:
%s`,
		profile.FullName, profile.YearsExperience,
		strings.Join(profile.TargetRoles, ", "), strings.Join(profile.Skills, ", "),
		profile.Summary,
		job.Title, job.Company, job.Location, job.Remote, job.JobType,
		truncate(job.Requirements, 2000), truncate(job.Description, 4000))
	return system, user
}

// buildCoverLetterPrompt returns the system and user prompts for cover letters.
func buildCoverLetterPrompt(job *models.Job, profile config.ProfileConfig, res *models.MatchResult) (system, user string) {
	system = "You are an expert career writer. Write a concise, specific, professional " +
		"cover letter (180-250 words) in the first person. No placeholders, no markdown, " +
		"plain text only. Do not invent facts beyond the candidate profile."

	user = fmt.Sprintf(`Write a cover letter for this application.

CANDIDATE
Name: %s
Years of experience: %d
Skills: %s
Summary: %s
Strengths for this role: %s

JOB
Title: %s at %s
Location: %s
Requirements: %s`,
		profile.FullName, profile.YearsExperience, strings.Join(profile.Skills, ", "),
		profile.Summary, strings.Join(res.Strengths, ", "),
		job.Title, job.Company, job.Location, truncate(job.Requirements, 1500))
	return system, user
}

// parseScore converts raw model text into a MatchResult, clamping the score.
func parseScore(text string) (*models.MatchResult, error) {
	var sr scoreResponse
	if err := json.Unmarshal([]byte(extractJSON(text)), &sr); err != nil {
		return nil, fmt.Errorf("parse score JSON: %w (raw: %s)", err, truncate(text, 300))
	}
	if sr.Score < 0 {
		sr.Score = 0
	}
	if sr.Score > 1 {
		sr.Score = 1
	}
	return &models.MatchResult{
		Score:        sr.Score,
		Reasoning:    sr.Reasoning,
		KeySkillGaps: sr.KeySkillGaps,
		Strengths:    sr.Strengths,
	}, nil
}
