---
title: "监听聊天"
weight: 4
---

# 监听聊天

{{< hint warning >}}
该功能需开启 UserBot 集成.
{{< /hint >}}

监听指定聊天的消息, 并自动保存到默认存储中, 遵从存储规则, 并且可以设置过滤器来只保存匹配的消息.

监听聊天:

```
/watch <chat_id/username> [msgre:<正则>] [mediatype:<类型>]
```

取消监听:

```
/unwatch <chat_id/username>
```

过滤器类型:

## msgre

正则匹配消息文本, 例如:

```
/watch 12345678 msgre:.*hello.*
```

这将会监听 ID 为 12345678 的聊天, 并且只保存消息文本中包含 "hello" 的消息.

## mediatype

逗号分隔的媒体类型列表, 只保存这些类型的消息, 例如:

```
/watch 12345678 mediatype:photo,audio
```

这将监听该聊天, 并且只保存图片和音频文件. 有效类型: `photo`, `video`, `animation`, `audio`, `voice`, `sticker`, `document` (别名: `gif`, `music`, `vid`, `img`, `doc`).

两种过滤器可以组合使用, 且媒体类型检查先于正则执行:

```
/watch 12345678 msgre:.*hello.* mediatype:video
```

监听未设置 `mediatype:` 时, 将应用用户在 `/config` 中设置的默认媒体过滤器.

{{< hint warning >}}
创建监听时, 未知的媒体类型会被立即拒绝 — 不会创建一个永远不会匹配的过滤器.
{{< /hint >}}

## 任务通知

所有用户的监听任务通知默认关闭. 开启后, 机器人从被监听聊天中保存的每条消息都会在此处收到通知, 并随下载进度更新, 最终显示保存结果:

```
/watch notify on
```

使用 `/watch notify off` 关闭, 使用 `/watch notify` 查询当前状态.

通知由机器人发送到你与它的私聊中. 该开关按用户生效, 而非按聊天: `/watch notify off` 会关闭该用户所有被监听聊天的通知.
