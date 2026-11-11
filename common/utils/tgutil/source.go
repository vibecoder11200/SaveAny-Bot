package tgutil

import (
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// SourceFromTGFile reports the Telegram chat and message a file came from
// (0, 0 for files built outside a source message).
func SourceFromTGFile(file tfile.TGFile) (chatID int64, messageID int64) {
	msgFile, ok := file.(tfile.TGFileMessage)
	if !ok || msgFile.Message() == nil {
		return 0, 0
	}
	msg := msgFile.Message()
	return ChatIdFromPeer(msg.GetPeerID()), int64(msg.ID)
}
