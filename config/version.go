package config

// inject version by '-X' flag
// go build -ldflags "-X github.com/krau/SaveAny-Bot/config.Version=${{ env.VERSION }}"
var (
	Version   string = "dev"
	BuildTime string = "unknown"
	GitCommit string = "unknown"
	Docker    string = "false" // whether built inside Docker
)

const (
	GitRepo = "vibecoder11200/SaveAny-Bot"
)
