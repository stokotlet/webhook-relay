package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL, APIKey, HTTPAddr, AdminAddr string
	Workers                                  int
	AllowPrivate                             bool
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), APIKey: os.Getenv("API_KEY"), HTTPAddr: env("HTTP_ADDR", ":8080"), AdminAddr: env("ADMIN_ADDR", "127.0.0.1:9090")}
	var err error
	c.Workers, err = strconv.Atoi(env("WORKERS", "4"))
	if err != nil || c.Workers < 1 || c.Workers > 64 {
		return c, fmt.Errorf("WORKERS must be between 1 and 64")
	}
	c.AllowPrivate, err = strconv.ParseBool(env("ALLOW_PRIVATE_TARGETS", "false"))
	if err != nil {
		return c, fmt.Errorf("ALLOW_PRIVATE_TARGETS must be a boolean")
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if len(c.APIKey) < 24 {
		return c, fmt.Errorf("API_KEY must contain at least 24 characters")
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
