package mediautil

import (
	"strings"

	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
)

// MatchStringForFilter builds the string a msgre filter matches against,
// byte-identical to what /save batch matches: the message caption, a single
// space, then the raw media filename (tgutil.GetMediaFileName output — the
// document id/mime fallback included, NOT the sanitized tfile name). /save
// and /watch must agree on this builder or a regex saved in one place
// silently mismatches in the other.
func MatchStringForFilter(msg *tg.Message) string {
	var sb strings.Builder
	sb.WriteString(msg.GetMessage())
	sb.WriteString(" ")
	if media, ok := msg.GetMedia(); ok {
		fn, _ := tgutil.GetMediaFileName(media)
		sb.WriteString(fn)
	}
	return sb.String()
}
