---
title: "Usage"
weight: 10
---

# Usage

This page introduces some of Save Any Bot's features and basic usage. If you can't find what you need here, please also see the [Configuration Guide](../deployment/configuration) or ask in GitHub [Discussions](https://github.com/krau/SaveAny-Bot/discussions).

## File Transfer

To use the bot's Telegram file saving feature, you need to send or forward the following types of messages to the bot:

1. File or media messages, such as images, videos, documents, etc.
2. Telegram message links, for example: `https://t.me/acherkrau/1097`. **Even if the channel prohibits forwarding and saving, the bot can still download its files.**
3. Telegra.ph article links. The bot will download all images in the article.

## Batch Saving With Filters

After setting a default storage, you can batch save a message range from a chat:

```
/save <channel_id/username> <message_id_range> [t:types] [regex]
```

The optional third argument accepts a media type filter with the `t:` prefix, and an optional fourth argument is a caption/filename regex:

```
/save @acherkrau 114-514 t:video,photo
/save @acherkrau 114-514 t:video ^trailer$
/save @acherkrau 114-514 ^t:my_regex_note$
```

Valid types: `photo`, `video`, `animation`, `audio`, `voice`, `sticker`, `document` (aliases: `gif`, `music`, `vid`, `img`, `doc`). An argument starting with `t:` is always treated as a media type filter — an unknown type is rejected immediately; to regex-match a literal `t:` at the start of a caption, anchor it as `^t:`.

When `t:` is omitted, your default media filter set via `/config` applies. Manually forwarded messages are never filtered.