# Telegram 备份下载工具

[English](./README.md)

支持把消息 （格式为 [JSON](#格式)）和媒体从特定的对话、群组、频道中导出。

它只能备份那些已经下载到本地的消息，如果有被中断下载的文件，将会使其恢复下载状态。

此工具的工作方式就像是一个 Telegram 的客户端，所以你必须输入手机号，接着是验证码和密码（如果设置的话）

此工具**不会获取频道的评论**。如果你需要评论内容的话，你必须加入频道的评论群组。

## 安装

```
go get github.com/3bl3gamer/tg-history-dumper
tg_history_dumper [args]
```

或者
```
git clone https://github.com/3bl3gamer/tg-history-dumper
cd tg-history-dumper
go build
./tg_history_dumper [args]
```

## 使用方式

### 准备工作

`app_id` 和 `app_hash` 是必须的. 如何获取请参考 https://core.telegram.org/api/obtaining_api_id#obtaining-api-id

### 最简单的使用方式

```tg_history_dumper -app-id=12345 -app-hash=abcdefg```

它会将获取到的凭证信息存储在 `tg.session` 文件中并且开始下载所有对话框（默认不包括媒体文件）数据到`history`文件夹。

### 配置

默认配置信息将会从`config.json`文件中读取。如果你起了不同的名称，可以使用`-config` [参数](#命令行参数)将其告诉此工具。

格式:

```json
{
    "app_id": 12345,
    "app_hash": "abcdefg",
    "socks5_proxy_addr": "127.0.0.1:9050",
    "request_interval_ms": 1000,
    "session_file_path": "tg.session",
    "out_dir_path": "history",
    "history": [
        "all",
        {"exclude": {"type": "channel"}},
        {"username": "my_channel"}
    ],
    "media": [
        "none",
        {"type": "user"},
        {"username": "my_channel"}
    ],
    "dump_account": "off",
    "dump_contacts": "off",
    "dump_sessions": "off"
}
```

* `app_id` 和 `app_hash` — 参考 [准备工作](#准备工作);
* `socks5_proxy_addr` — (可选) `address:post` 对应 SOCKS5 端口;
* `request_interval_ms` — (可选, 默认值 1000) 请求历史消息数据的时间间隔（可能会减小，尽管这可能并不会加快处理速度，毕竟 TG 那边对请求频率做了限制。）
* `session_file_path` — (可选, 默认 `tg.session`) session 文件的位置（这个文件是为了让你下次不必重新登录）
* `out_dir_path` — (可选, 默认 `history`) 存储历史消息和媒体文件的文件夹。
* `history` — (可选, 默认 `{"type": "user"}`) 聊天过滤[规则](#规则)。
* `media` — (可选, 默认 `"none"`) 聊天过滤[规则](#规则)，仅在符合`history`规则的聊天中生效。
* `history_limit` — (optional, default is `{}`) new chat [history limiting](#history-limits) rules;
* `dump_account` — (optional, default is `"off"`, use `"write"` to enable dump) dumps basic account information to file, overrides config.dump_account, does not apply when `-list-chats` enabled;
* `dump_contacts` — (optional, default is `"off"`, use `"write"` to enable dump) dumps contacts information to file, overrides config.dump_contacts, does not apply when `-list-chats` enabled;
* `dump_sessions` — (optional, default is `"off"`, use `"write"` to enable dump) dumps active sessions to file, overrides config.dump_sessions, does not apply when `-list-chats` enabled.

如果配置中有了 `app_id` 和 `app_hash`，那么此工具就不会使用命令行参数中的那个了，你也没必要在命令行参数中再附带这两个参数。


### History limits

Limits define how many messages will be dumped for chats for the first time.
They are configured as limit_count:[rules](#rules).
If chat matches more than one rule, the lower limit is applied.
If chat does not match any rules, all messages are dumped.
If there are already some messages from previous dump for the chat, its limits are ignored.

For example, this config sets limit to 5000 for groups, 10000 for channels, dialogs remain unlimited:

```json
"history_limit": {
    "5000": {"type": "group"},
    "10000": {"type": "channel"}
}
```


### 规则

这些规则会被使用在过滤特殊的对话中（或者过滤这些对话中的媒体文件中）。

如果命中（备份规则）一些规则，并且后面也没有被另一些规则反向命中（不备份的规则）掉 ，对话/文件就会被备份下来。没有匹配到规则的数据将不会被备份。



比如，这个规则就是仅备份对：

```json
"history": {"type": "user"}
```

接受所有的对话，除了频道（注意，不一定只是一个规则。多个规则是从前向后应用）：

```json
"history": [
    "all",
    {"exclude": {"type": "channel"}}
]
```

接受对话和群组中的所有媒体文件，但是群组中的媒体文件有 500MiB 的大小限制：

```json
"media": [
    {"type": "user"},
    {"type": "group", "media_max_size": "500M"}
]
```

接受所有对话中的媒体文件和两个群组中的媒体文件，两个群组媒体文件的大小限制是 500MiB：

```json
"media": [
    {"type": "user"},
    {"only": [
        {"title": "Group A"},
        {"title": "Group B"},
    ], "with": {"media_max_size": "500M"}}
]
```

`only`规则也可以这么写：

```json
{"title": "Group A", "media_max_size": "500M"},
{"title": "Group B", "media_max_size": "500M"}
```

#### 属性的规则

```json
{
    "id": 123,
    "title": "Name",
    "username": "uname",
    "type": "user",
    "media_max_size": "500M"
}
```

通过下列的属性来匹配聊天和文件：

* `id` 可以从 [聊天列表](#聊天列表) 获取。
* `title` 对于用户来说就是`名 + 姓`
* `type` 可以是 `"user"`, `"group"` 或 `"channel"`;
* `media_max_size`仅会被用在 `config.media` 并且必须类似这种形式 `"500M"`, `"500K"` 或 `"500"` (字节).

#### 排除规则

```json
{"exclude": "inner rule"}
```

将会从聊天/文件的匹配规则中排除掉，即使他们前面已经被匹配上了。

#### 列表规则

```json
["rule0", "rule1", "more rules"]
```

一个一个地应用列表中的规则。（前面也提到过，从前往后地应用）

#### Only 规则

```json
{"only": "only-rule", "with": "with-rule"}
```

仅在`only-rule`规则匹配到时才会使用`with-rule`规则。

#### All 规则

```json
"all"
```

匹配一切。

#### None 规则

```json
"none"
```

什么都不匹配。

### 聊天列表

`tg_history_dumper -list-chats`

将聊天以 `<type> <id> <title> (<username>)` 格式输出。
对于用户来说，`Title `就是用户名。

如果聊天没有匹配`config.history`规则，那么输出命令行日志时改行将会变灰。

### 命令行参数

一些参数将会覆盖`config`中的值。

比如`-chat='Some Chat'`将会覆盖`config.history`中的对应值，这意味着数据更新时将只会处理来自`Some Chat`的。

```
$ tg_history_dumper --help
Usage of tg_history_dumper:
  -app-hash string
      app hash
  -app-id int
      app id
  -chat string
      被备份的聊天的标题，将会覆盖 config.history
  -config string
      配置文件的路径 (默认 "config.json")
  -debug
      展示 debug 相关的日志信息。
  -debug-tg
      展示 debug TGClient 的日志信息。
  -dump-account string
        enable basic user information dump, use 'write' to enable dump, overriders config.dump_account
  -dump-contacts string
        enable contacts dump, use 'write' to enable dump, overriders config.dump_contacts
  -dump-sessions string
        enable active sessions dump, use 'write' to enable dump, overriders config.dump_sessions
  -list-chats
      列出来所有可用的聊天
  -out string
      备份目录的路径，将会覆盖 config.out_dir_path
  -session string
      session 文件路径，将会覆盖 config.session_file_path
  -socks5 string
      socks5 配置，包括 地址:端口，将会覆盖 config.socks5_proxy_addr
```

## 格式

## 消息

所有的消息将会以 JSON 数据存储在 `history/<id>_<title>`，此工具将会只会通过 id 来搜索文件夹，并且当 `title` 变化时重命名文件夹。

每个 JSON 对象都会有一个 `"_" 属性，`最外层的对象还有一个 `"_TL_LAYER"` 字段，其值是 API 的版本号。比如：

```json
{"Date":1601491406,"Message":"Hello World!","PeerID":{"ChannelID":1261507434,"_":"TL_peerChannel"},"_":"TL_message","_TL_LAYER":119}
```

（为了方便阅读，把其他很多属性都删除掉了）

## Peers

Related users and chats are saved to `history/users` and `history/chats` respectively. Each file is JSON Lines with some basic user/chat data like id, usrname, first/lastname, title, etc.

Lines are added not only when new peer is encountered but also when existing peer data (title for example) has changed from previous dump. So same users/chats may appear multiple times there. The last record for each id is the most recent one.