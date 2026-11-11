package handlers

import (
	"errors"
	"regexp"
	"strings"
	"sync"
)

// Watch flag prefixes. A token starting with one of these is a flag, not a
// positional argument; the value is the rest of the token.
const (
	watchFlagMsgRe     = "msgre:"
	watchFlagMediaType = "mediatype:"
	watchFlagStorage   = "storage:"
	watchFlagDir       = "dir:"
)

// maxWatchRegexLen caps a stored watch regex. Compiled patterns allocate
// proportional to their source length; live events and catch-up feed every
// observed message through them, so an unbounded pattern would let a single
// watch pin arbitrary memory and CPU on every event.
const maxWatchRegexLen = 512

// watchArgs holds the parsed arguments of a /watch command.
type watchArgs struct {
	Chat        string
	MsgRe       string
	MediaType   string
	StorageName string
	DirPath     string
}

// parseWatchArgs errors, mapped by the caller to localized replies.
var (
	errWatchFilterTypeUnsupported = errors.New("unsupported filter type")
	errWatchFilterFormatInvalid   = errors.New("invalid filter format")
)

// isWatchFlagToken reports whether the token is a flag; a flag always ends a
// multi-word msgre regex.
func isWatchFlagToken(token string) bool {
	return strings.HasPrefix(token, watchFlagMsgRe) ||
		strings.HasPrefix(token, watchFlagMediaType) ||
		strings.HasPrefix(token, watchFlagStorage) ||
		strings.HasPrefix(token, watchFlagDir)
}

// parseWatchArgs splits /watch args into the target chat and flag values.
// Grammar: /watch <chat> [storage:<name>] [dir:<path>] [msgre:<regex>]
// [mediatype:<types>] — flags in any order, all optional. The msgre value is
// the rest of its own token plus every following non-flag token joined with
// " ", so regexes may contain ':' and span words; a regex ends at the next
// flag token. storage:/dir: are single-token flags (quoted values survive
// because the command line is split with a quote-aware splitter). The first
// non-flag token is the chat; a later non-flag token outside a msgre tail is
// a malformed leftover, and a later unknown "<x>:" token is an unsupported
// filter type.
func parseWatchArgs(args []string) (watchArgs, error) {
	var parsed watchArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, watchFlagMsgRe):
			parsed.MsgRe = strings.TrimPrefix(arg, watchFlagMsgRe)
			for i+1 < len(args) && !isWatchFlagToken(args[i+1]) {
				i++
				parsed.MsgRe += " " + args[i]
			}
		case strings.HasPrefix(arg, watchFlagMediaType):
			parsed.MediaType = strings.TrimPrefix(arg, watchFlagMediaType)
		case strings.HasPrefix(arg, watchFlagStorage):
			parsed.StorageName = strings.TrimPrefix(arg, watchFlagStorage)
		case strings.HasPrefix(arg, watchFlagDir):
			parsed.DirPath = strings.TrimPrefix(arg, watchFlagDir)
		default:
			if parsed.Chat == "" {
				parsed.Chat = arg
				continue
			}
			if strings.Contains(arg, ":") {
				return parsed, errWatchFilterTypeUnsupported
			}
			return parsed, errWatchFilterFormatInvalid
		}
	}
	return parsed, nil
}

// watchRegexCache caches compiled watch filters keyed by the stored filter
// string ("msgre:<regex>"). Live events and catch-up backfill both compile
// per observed message, and regexp.Compile is far too expensive for that —
// the cache makes it once per watch. Watches are few and their filters
// stable, so entries live for the process lifetime.
var watchRegexCache sync.Map

// watchFilterRegex returns the compiled regex of a stored watch filter. The
// second result is false when the filter is not a msgre filter or its regex
// does not compile — stored filters are validated at creation, so a compile
// failure here means corrupt data and the caller must match nothing.
func watchFilterRegex(filter string) (*regexp.Regexp, bool) {
	data, ok := strings.CutPrefix(filter, watchFlagMsgRe)
	if !ok {
		return nil, false
	}
	if cached, ok := watchRegexCache.Load(filter); ok {
		return cached.(*regexp.Regexp), true
	}
	regex, err := regexp.Compile(data)
	if err != nil {
		return nil, false
	}
	watchRegexCache.Store(filter, regex)
	return regex, true
}
