package tgutil

import "github.com/krau/SaveAny-Bot/pkg/tfile"

// SourceFromTGFile derives the (chat, message) origin of a task file for
// the saved-file ledger: 0,0 when the file has no Telegram message behind
// it (constructed from a reader, or the message already dropped).
func SourceFromTGFile(file tfile.TGFile) (chatID int64, messageID int) {
	msgFile, ok := file.(tfile.TGFileMessage)
	if !ok || msgFile.Message() == nil {
		return 0, 0
	}
	msg := msgFile.Message()
	return ChatIdFromPeer(msg.GetPeerID()), msg.ID
}
