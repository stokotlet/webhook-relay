package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/relay")
	t.Setenv("API_KEY", "long-enough-development-key")
	t.Setenv("WORKERS", "4")
	t.Setenv("ALLOW_PRIVATE_TARGETS", "false")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "65", "oops"} {
		t.Setenv("WORKERS", value)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted WORKERS=%s", value)
		}
	}
	t.Setenv("WORKERS", "4")
	t.Setenv("API_KEY", "short")
	if _, err := Load(); err == nil {
		t.Fatal("short key accepted")
	}
}
