package storage_test

import (
	"testing"

	storcfg "github.com/krau/SaveAny-Bot/config/storage"
)

func TestTelegramStorageConfigValidate(t *testing.T) {
	base := func() *storcfg.TelegramStorageConfig {
		return &storcfg.TelegramStorageConfig{
			BaseConfig: storcfg.BaseConfig{Name: "tg"},
			ChatID:     -1001234,
		}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("valid config must pass: %v", err)
	}

	// Topic targeting is opt-in: a positive forum root message id is valid.
	cfg := base()
	cfg.TopicID = 42
	if err := cfg.Validate(); err != nil {
		t.Fatalf("topic id must be accepted: %v", err)
	}

	// A negative id can only be a mistake; catch it at load time.
	cfg = base()
	cfg.TopicID = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative topic id must be rejected")
	}

	noChat := base()
	noChat.ChatID = 0
	if err := noChat.Validate(); err == nil {
		t.Fatal("missing chat id must be rejected")
	}
}
