package config

type taskConfig struct {
	// Recovery re-enqueues TG-file tasks that were in flight when the bot
	// last stopped (journal-backed).
	Recovery bool `toml:"recovery" mapstructure:"recovery" json:"recovery"`
}
