# Telegram History Dumber

Exports messages (as [JSON](#format)) and media from specified dialogs, groups and channels.

It gets only new (after already fetched) messages and resumes file downloads (if interrupted).

It works as a Telegram client. So yes, you will have to enter you phone, confirmation code and password (if any).

It **will not fetch channel comments**. If you need them, you should join channel's dicussion group.

## Installing

```
go get github.com/3bl3gamer/tg-history-dumper
tg_history_dumper [args]
```

Or
```
git clone https://github.com/3bl3gamer/tg-history-dumper
cd tg-history-dumper
go build
./tg_history_dumper [args]
```

## Usage

### Preparing

`app_id` and `app_hash` must be obtained from Telegram. More info at https://core.telegram.org/api/obtaining_api_id#obtaining-api-id

### Simple

```tg_history_dumper -app-id=12345 -app-hash=abcdefg```

It will ask credentials, save session to `tg.session` file and download all dialogs (without media) to `history` folder.

### Config

...is read from `config.json`, different file may be provided via `-config` [argument](#arguments).

Format:
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
}
```

* `app_id` and `app_hash` — see [preparing](#preparing);
* `socks5_proxy_addr` — (optional) `address:post` of SOCKS5 proxy;
* `request_interval_ms` — (optional, default is 1000) interval for requesting history message chunks (may be decreased, though it likely will not speed up the process, since TG has query rate limits);
* `session_file_path` — (optional, default is `tg.session`) session file location (you will not have to login next time if it is present);
* `out_dir_path` — (optional, default is `history`) folder for saved messages and media;
* `history` — (optional, default is `{"type": "user"}`) chat filtering [rules](#rules);
* `media` — (optional, default is `"none"`) chat media filtering [rules](#rules), only applies to chats matched to `history` rules.

If config has non-empty `app_id` and `app_hash`, dump may be updated just with `tg_history_dumper` (without arguments).

### Rules

Rules used to accept/reject specific chats (or media in these chats).
Chat/file is accepted if it matches to some rules and not later excluded by others. Everything is rejected by default.

For example, this rule accepts only dialogs:

```json
"history": {"type": "user"}
```

Accepts all chats except channels:

```json
"history": [
    "all",
    {"exclude": {"type": "channel"}}
]
```

Accepts all media from dialogs and group chats but group media size is limited to 500 MiB:

```json
"media": [
    {"type": "user"},
    {"type": "group", "media_max_size": "500M"}
]
```

Accepts all media from dialogs and two groups, groups media size is limited to 500 MiB:

```json
"media": [
    {"type": "user"},
    {"only": [
        {"title": "Group A"},
        {"title": "Group B"},
    ], "with": {"media_max_size": "500M"}}
]
```

`only`-rule may be rewritten as:
```json
{"title": "Group A", "media_max_size": "500M"},
{"title": "Group B", "media_max_size": "500M"}
```

#### Attributes rule

```json
{
    "id": 123,
    "title": "Name",
    "username": "uname",
    "type": "user",
    "media_max_size": "500M"
}
```

Matches chat/file by all provided attributes.
* `id` can be obtained from [chats list](#listing-chats);
* `title` for users is `"FirstName LastName"`;
* `type` may be `"user"`, `"group"` or `"channel"`;
* `media_max_size` is only used in `config.media` and must be in form `"500M"`, `"500K"` or `"500"` (for bytes).

#### Exclude rule

```json
{"exclude": "inner rule"}
```
Excludes chats/files from match even if they matched some previous rule.

#### List rule

```json
["rule0", "rule1", "more rules"]
```
Applies inner rules one by one.

#### Only rule

```json
{"only": "only-rule", "with": "with-rule"}
```

Tries `with-rule` only if `only-rule` matched.

#### All rule

```json
"all"
```

Matches everything.

#### None rule

```json
"none"
```

Matches nothing.

### Listing chats

`tg_history_dumper -list-chats`

Outputs chats in format `<type> <id> <title> (<username>)`.
Title for users is `FirstName LastName`.
If chat does not match `config.history` rules, the line is grayed out.

### Arguments

Some arguments override values from `config`.
For example `-chat='Some Chat'` may be used to override `config.history`
and update messages only from `Some Chat`.

```
$ tg_history_dumper --help
Usage of tg_history_dumper:
  -app-hash string
      app hash
  -app-id int
      app id
  -chat string
      title of the chat to dump, overrides config.history
  -config string
      path to config file (default "config.json")
  -debug
      show debug log messages
  -debug-tg
      show debug TGClient log messages
  -list-chats
      list all available chats
  -out string
      output directory path, overriders config.out_dir_path
  -session string
      session file path, overrides config.session_file_path
  -socks5 string
      socks5 proxy address:port, overrides config.socks5_proxy_addr
```

## Format

## Messages

All messages are saved as JSON Lines (aka jsonl) to file `history/<id>_<title>`. Dumper searches directories only by id and renames folder when title is changed.

Each JSON object has special field `"_"` with type name. Outermost objects has one more special field `"_TL_LAYER"` with layer number (API version). For example:

```json
{"Date":1601491406,"Message":"Hello World!","PeerID":{"ChannelID":1261507434,"_":"TL_peerChannel"},"_":"TL_message","_TL_LAYER":119}
```
(some message fields were removed for readability)

## Peers

Related users and chats are saved to `history/users` and `history/chats` respectively. Each file is JSON Lines with some basic user/chat data like id, usrname, first/lastname, title, etc.

Lines are added not only when new peer is encountered but also when existing peer data (title for example) has changed from previous dump. So same users/chats may appear multiple times there. The last record for each id is the most recent one.
