package mediautil

import (
	"github.com/charmbracelet/log"

	"github.com/krau/SaveAny-Bot/common/mediafilter"
	"github.com/krau/SaveAny-Bot/database"
)

// UserMediaFilter resolves the per-user default media filter from the
// stored column. Stored values are only ever written through validated
// paths (the API users PATCH and the t: argument parse), so a parse failure
// here means corrupt data: log a warning and return nil (allow everything)
// rather than brick the handler.
func UserMediaFilter(user *database.User) mediafilter.MediaFilter {
	if user == nil || user.MediaFilter == "" {
		return nil
	}
	filter, err := mediafilter.ParseMediaFilter(user.MediaFilter)
	if err != nil {
		log.Warnf("invalid stored media filter for user %d: %v", user.ChatID, err)
		return nil
	}
	return filter
}
