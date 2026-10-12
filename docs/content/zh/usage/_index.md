---
title: "使用帮助"
weight: 10
---

# 使用帮助

这里介绍 Save Any Bot 的一些功能和使用方法, 如果你没有在这里找到你需要的内容, 另请参阅 [配置说明](../deployment/configuration) 或前往 Github [Discussions](https://github.com/krau/SaveAny-Bot/discussions) 提问.

## 转存文件

要使用 Bot 的转存 Telegram 文件功能, 需要向 Bot 发送或转发以下类型的消息.

1. 文件或媒体消息, 如图片, 视频, 文档等
2. Telegram 消息链接, 例如: `https://t.me/acherkrau/1097`. **即使频道禁止了转发和保存, Bot 依然可以下载其文件.**
3. Telegra.ph 的文章链接, Bot 将下载其中的所有图片

## 批量保存与过滤器

设置默认存储后, 可以批量保存一个聊天中的消息范围:

```
/save <频道ID/用户名> <消息ID范围> [t:类型] [正则]
```

可选的第三个参数支持带 `t:` 前缀的媒体类型过滤器, 再后面的可选参数为说明文字/文件名的正则:

```
/save @acherkrau 114-514 t:video,photo
/save @acherkrau 114-514 t:video ^trailer$
/save @acherkrau 114-514 ^t:my_regex_note$
```

有效类型: `photo`, `video`, `animation`, `audio`, `voice`, `sticker`, `document` (别名: `gif`, `music`, `vid`, `img`, `doc`). 以 `t:` 开头的参数始终被视为媒体类型过滤器 — 未知的类型会被立即拒绝; 若要用正则匹配以字面量 `t:` 开头的文字, 请写成 `^t:`.

省略 `t:` 时, 将应用在 `/config` 中设置的默认媒体过滤器. 手动转发的消息不会被过滤.