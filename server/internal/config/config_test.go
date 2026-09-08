package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "PG_HOST", "PG_PORT", "PG_USER", "PG_PASSWORD", "PG_DB", "QDRANT_URL"} {
		t.Setenv(k, "") // 置空 = 视为未设置
	}
	c := Load()
	if c.Port != "8080" {
		t.Errorf("Port = %q, want 8080", c.Port)
	}
	if c.PGHost != "localhost" || c.PGPort != "5432" || c.PGUser != "eval" ||
		c.PGPassword != "eval_dev_password" || c.PGDB != "eval_platform" {
		t.Errorf("PG 默认值不符: %+v", c)
	}
	if c.QdrantURL != "http://localhost:6333" {
		t.Errorf("QdrantURL = %q, want http://localhost:6333", c.QdrantURL)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("PG_USER", "u2")
	c := Load()
	if c.Port != "9090" || c.PGUser != "u2" {
		t.Errorf("环境变量覆盖未生效: %+v", c)
	}
}

func TestPostgresDSN(t *testing.T) {
	t.Setenv("PG_HOST", "h")
	t.Setenv("PG_PORT", "1")
	t.Setenv("PG_USER", "u")
	t.Setenv("PG_PASSWORD", "p")
	t.Setenv("PG_DB", "d")
	t.Setenv("PORT", "")
	t.Setenv("QDRANT_URL", "")
	c := Load()
	want := "postgres://u:p@h:1/d?sslmode=disable"
	if got := c.PostgresDSN(); got != want {
		t.Errorf("PostgresDSN() = %q, want %q", got, want)
	}
}
