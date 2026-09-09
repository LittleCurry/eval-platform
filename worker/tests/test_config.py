from app.config import Settings


def test_defaults_without_env_file():
    s = Settings(_env_file=None)
    assert s.pg_dsn.startswith("postgresql://eval:")
    assert s.qdrant_url == "http://localhost:6333"
    assert s.log_level == "INFO"


def test_env_override(monkeypatch):
    monkeypatch.setenv("PG_DSN", "postgresql://other:secret@h:1/d")
    s = Settings(_env_file=None)
    assert s.pg_dsn == "postgresql://other:secret@h:1/d"