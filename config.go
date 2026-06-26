package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all runtime settings. Everything is driven by environment
// variables so the same binary runs unchanged on any local machine.
type Config struct {
	Port        string // FLEETBOARD_PORT (default 7777)
	ClaudeHome  string // CLAUDE_HOME (default ~/.claude)
	MaxAgeHours int    // FLEETBOARD_MAX_AGE_HOURS: hide sessions idle longer than this (default 168 = 7d)
	RunningSec  int    // FLEETBOARD_RUNNING_SEC: activity newer than this counts as "running" (default 45)
	WaitingHrs  int    // FLEETBOARD_WAITING_HOURS: assistant-ended session older than this is "idle", not "waiting" (default 24)
	ExtraRepos  []string

	ADOOrg     string // ADO_ORG
	ADOProject string // ADO_PROJECT
	ADOPat     string // ADO_PAT
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func loadConfig() Config {
	home, _ := os.UserHomeDir()
	c := Config{
		Port:        env("FLEETBOARD_PORT", "7777"),
		ClaudeHome:  env("CLAUDE_HOME", filepath.Join(home, ".claude")),
		MaxAgeHours: envInt("FLEETBOARD_MAX_AGE_HOURS", 168),
		RunningSec:  envInt("FLEETBOARD_RUNNING_SEC", 45),
		WaitingHrs:  envInt("FLEETBOARD_WAITING_HOURS", 24),
		ADOOrg:      env("ADO_ORG", ""),
		ADOProject:  env("ADO_PROJECT", ""),
		ADOPat:      env("ADO_PAT", ""),
	}
	// FLEETBOARD_REPOS: extra repo paths (comma or colon separated) to show
	// even when no Claude session currently targets them.
	if raw := strings.TrimSpace(os.Getenv("FLEETBOARD_REPOS")); raw != "" {
		for _, p := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ':' }) {
			if p = strings.TrimSpace(p); p != "" {
				c.ExtraRepos = append(c.ExtraRepos, p)
			}
		}
	}
	return c
}

func (c Config) adoConfigured() bool {
	return c.ADOOrg != "" && c.ADOProject != "" && c.ADOPat != ""
}
