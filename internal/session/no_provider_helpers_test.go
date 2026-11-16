package session

import (
	"io"
	"log/slog"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func emptyConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{}
	cfg.Route = map[string]config.Route{}
	return cfg
}

func configuredConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"fake": {
			Kind:    "openai-compat",
			BaseURL: "http://127.0.0.1:1/v1",
			Models:  map[string]config.Model{"m": {ContextWindow: 8000, Tools: true}},
		},
	}
	cfg.Route = map[string]config.Route{
		"chat": {Candidates: []string{"fake/m"}},
	}
	return cfg
}
