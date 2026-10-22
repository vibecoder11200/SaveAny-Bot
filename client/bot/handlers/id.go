package handlers

import (
	"fmt"
	"strings"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/types"
	"github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
)

// handleIdCmd resolves a @username to the chat's title, type and numeric ID
// accepted by /save. Prefers the userbot so public channels the bot is not
// a member of resolve too.
func handleIdCmd(ctx *ext.Context, update *ext.Update) error {
	args := strings.Fields(string(update.EffectiveMessage.Text))
	if len(args) != 2 || !strings.HasPrefix(args[1], "@") || len(args[1]) < 2 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgChatidUsage)), nil)
		return dispatcher.EndGroups
	}
	tctx := ctx
	if uctx := user.GetCtx(); uctx != nil {
		tctx = uctx
	}
	chat, err := tctx.ResolveUsername(args[1])
	if err != nil {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgChatidErrorResolveFailed, map[string]any{
			"Error": err.Error(),
		})), nil)
		return dispatcher.EndGroups
	}
	if chat.GetID() == 0 {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgChatidErrorResolveFailed, map[string]any{
			"Error": fmt.Sprintf("no chat found for username: %s", args[1]),
		})), nil)
		return dispatcher.EndGroups
	}
	title, typ := chatTitleAndType(chat)
	ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgChatidInfo, map[string]any{
		"Title": title,
		"Type":  typ,
		"ID":    chat.GetID(),
	})), nil)
	return dispatcher.EndGroups
}

func chatTitleAndType(chat types.EffectiveChat) (title, typ string) {
	switch c := chat.(type) {
	case *types.Channel:
		if c.Megagroup {
			return c.Title, "group"
		}
		return c.Title, "channel"
	case *types.User:
		return strings.TrimSpace(c.FirstName + " " + c.LastName), "user"
	case *types.Chat:
		return c.Title, "chat"
	}
	return "", "unknown"
}
