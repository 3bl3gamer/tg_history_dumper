package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"

	"github.com/ansel1/merry/v2"
)

var defaultConfig = &Config{
	History:           ConfigChatFilterType{Type: ChatUser},
	Stories:           ConfigChatFilterNone{},
	Media:             ConfigChatFilterNone{},
	RequestIntervalMS: 1000,
	SessionFilePath:   "tg.session",
	OutDirPath:        "history",
	DoAccountDump:     "off",
	DoContactsDump:    "off",
	DoSessionsDump:    "off",
}

type Config struct {
	AppID               int32
	AppHash             string
	History             ConfigChatFilter
	Stories             ConfigChatFilter
	HistoryLimit        ConfigChatHistoryLimit
	Media               ConfigChatFilter
	Socks5ProxyAddr     string
	Socks5ProxyUser     string
	Socks5ProxyPassword string
	RequestIntervalMS   int64
	SessionFilePath     string
	OutDirPath          string
	DoAccountDump       string
	DoContactsDump      string
	DoSessionsDump      string
}

type SuffuxedSize int64

func (s *SuffuxedSize) UnmarshalJSON(buf []byte) error {
	var str string
	if err := json.Unmarshal(buf, &str); err != nil {
		return merry.Wrap(err)
	}
	k := int64(1)
	l := len(str)
	if l > 0 && str[l-1] == 'K' {
		str = str[:l-1]
		k = 1024
	} else if l > 0 && str[l-1] == 'M' {
		str = str[:l-1]
		k = 1024 * 1024
	}
	n, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return merry.Wrap(err)
	}
	*s = SuffuxedSize(n * k)
	return nil
}

func (s *SuffuxedSize) MarshalJSON() ([]byte, error) {
	n := int64(*s)
	suffix := ""
	if n > 1024*1024 {
		n /= 1024 * 1024
		suffix = "M"
	} else if n > 1024 {
		n /= 1024
		suffix = "K"
	}
	return []byte(`"` + strconv.FormatInt(n, 10) + suffix + `"`), nil
}

type MatchResult int8

func (r MatchResult) String() string {
	switch r {
	case MatchTrue:
		return "True"
	case MatchUndefined:
		return "Undefined"
	case MatchFalse:
		return "False"
	default:
		return "???"
	}
}

const (
	MatchTrue MatchResult = iota - 1
	MatchUndefined
	MatchFalse
)

type ConfigChatFilter interface {
	Match(*Chat, *TGFileInfo) MatchResult
}

type ConfigChatFilterNone struct{}

func (f ConfigChatFilterNone) Match(chat *Chat, file *TGFileInfo) MatchResult { return MatchFalse }

type ConfigChatFilterAll struct{}

func (f ConfigChatFilterAll) Match(chat *Chat, file *TGFileInfo) MatchResult { return MatchTrue }

type ConfigChatFilterOnly struct {
	Only ConfigChatFilter
	With ConfigChatFilter
}

func (f ConfigChatFilterOnly) Match(chat *Chat, file *TGFileInfo) MatchResult {
	m := f.Only.Match(chat, file)
	if m == MatchTrue {
		m = f.With.Match(chat, file)
	}
	return m
}

type ConfigChatFilterMulti struct {
	Inner []ConfigChatFilter
}

func (f ConfigChatFilterMulti) Match(chat *Chat, file *TGFileInfo) MatchResult {
	res := MatchUndefined
	for _, innerF := range f.Inner {
		m := innerF.Match(chat, file)
		if m != MatchUndefined {
			res = m
		}
	}
	return res
}

type ConfigChatFilterExclude struct {
	Inner ConfigChatFilter
}

func (f ConfigChatFilterExclude) Match(chat *Chat, file *TGFileInfo) MatchResult {
	if f.Inner.Match(chat, file) == MatchTrue {
		return MatchFalse
	}
	return MatchUndefined
}

type ConfigChatFilterType struct{ Type ChatType }

func (f ConfigChatFilterType) Match(chat *Chat, file *TGFileInfo) MatchResult {
	if chat.Type == f.Type {
		return MatchTrue
	}
	return MatchUndefined
}

type ConfigChatFilterAttrs struct {
	ID           *int64        `json:"id,omitempty"`
	Title        *string       `json:"title,omitempty"`
	Username     *string       `json:"username,omitempty"`
	Type         *ChatType     `json:"type,omitempty"`
	MediaMaxSize *SuffuxedSize `json:"media_max_size,omitempty"`
}

func (f ConfigChatFilterAttrs) Match(chat *Chat, file *TGFileInfo) MatchResult {
	mc := (f.ID == nil || chat.ID == *f.ID) &&
		(f.Title == nil || chat.Title == *f.Title) &&
		(f.Username == nil || chat.Username == *f.Username) &&
		(f.Type == nil || chat.Type == *f.Type)
	mf := file == nil ||
		(f.MediaMaxSize == nil || int64(file.Size) <= int64(*f.MediaMaxSize))
	if mc && mf {
		return MatchTrue
	}
	return MatchUndefined
}

func (f ConfigChatFilterAttrs) String() string {
	buf, _ := json.Marshal(f)
	return string(buf)
}

type ConfigChatHistoryLimit map[int32]ConfigChatFilter

func (l ConfigChatHistoryLimit) For(chat *Chat) int32 {
	minLimit := int32(0)
	for limit, filter := range l {
		if minLimit == 0 || limit < minLimit {
			if filter.Match(chat, nil) == MatchTrue {
				minLimit = limit
			}
		}
	}
	return minLimit
}

type ConfigRaw struct {
	AppID               int32                     `json:"app_id"`
	AppHash             string                    `json:"app_hash"`
	History             json.RawMessage           `json:"history"`
	Stories             json.RawMessage           `json:"stories"`
	HistoryLimit        map[int32]json.RawMessage `json:"history_limit"`
	Media               json.RawMessage           `json:"media"`
	Socks5ProxyAddr     string                    `json:"socks5_proxy_addr"`
	Socks5ProxyUser     string                    `json:"socks5_proxy_user"`
	Socks5ProxyPassword string                    `json:"socks5_proxy_password"`
	RequestIntervalMS   int64                     `json:"request_interval_ms"`
	SessionFilePath     string                    `json:"session_file_path"`
	OutDirPath          string                    `json:"out_dir_path"`
	DoAccountDump       string                    `json:"dump_account"`
	DoContactsDump      string                    `json:"dump_contacts"`
	DoSessionsDump      string                    `json:"dump_sessions"`
}

var silentParseTestMode = false

func ParseConfig(fpath string) (*Config, error) {
	file, err := os.Open(fpath)
	if os.IsNotExist(err) {
		if !silentParseTestMode {
			log.Warn("config file %s not found, using default one", fpath)
		}
		return defaultConfig, nil
	} else if err != nil {
		return nil, merry.Wrap(err)
	}
	defer file.Close()

	raw := &ConfigRaw{}
	if err := json.NewDecoder(file).Decode(raw); err != nil {
		return nil, merry.Wrap(err)
	}

	cfg := &(*defaultConfig) //copying default
	cfg.AppID = raw.AppID
	cfg.AppHash = raw.AppHash
	cfg.Socks5ProxyAddr = raw.Socks5ProxyAddr
	cfg.Socks5ProxyUser = raw.Socks5ProxyUser
	cfg.Socks5ProxyPassword = raw.Socks5ProxyPassword

	if raw.RequestIntervalMS > 0 {
		cfg.RequestIntervalMS = raw.RequestIntervalMS
	}

	if raw.SessionFilePath != "" {
		cfg.SessionFilePath = raw.SessionFilePath
	}

	if raw.OutDirPath != "" {
		cfg.OutDirPath = raw.OutDirPath
	}

	if raw.DoAccountDump != "" {
		cfg.DoAccountDump = raw.DoAccountDump
	}

	if raw.DoContactsDump != "" {
		cfg.DoContactsDump = raw.DoContactsDump
	}

	if raw.DoSessionsDump != "" {
		cfg.DoSessionsDump = raw.DoSessionsDump
	}

	if len(raw.History) > 0 {
		cfg.History, err = parseConfigFilters(raw.History)
		if err != nil {
			return nil, merry.Wrap(err)
		}
	}

	if len(raw.Stories) > 0 {
		cfg.Stories, err = parseConfigFilters(raw.Stories)
		if err != nil {
			return nil, merry.Wrap(err)
		}
	}

	if len(raw.Media) > 0 {
		cfg.Media, err = parseConfigFilters(raw.Media)
		if err != nil {
			return nil, merry.Wrap(err)
		}
	}

	if len(raw.HistoryLimit) > 0 {
		cfg.HistoryLimit = make(map[int32]ConfigChatFilter, len(raw.HistoryLimit))
		for limit, rawFilter := range raw.HistoryLimit {
			cfg.HistoryLimit[limit], err = parseConfigFilters(rawFilter)
			if err != nil {
				return nil, merry.Wrap(err)
			}
		}
	}
	return cfg, nil
}

func parseConfigFilters(buf []byte) (ConfigChatFilter, error) {
	if bytes.Equal(buf, []byte(`"all"`)) {
		return ConfigChatFilterAll{}, nil
	}

	if bytes.Equal(buf, []byte(`"none"`)) {
		return ConfigChatFilterNone{}, nil
	}

	if buf[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(buf, &items); err != nil {
			return nil, merry.Wrap(err)
		}
		filters := make([]ConfigChatFilter, len(items))
		for i, item := range items {
			var err error
			filters[i], err = parseConfigFilters(item)
			if err != nil {
				return nil, merry.Wrap(err)
			}
		}
		return ConfigChatFilterMulti{filters}, nil
	}

	if buf[0] == '{' {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(buf, &item); err != nil {
			return nil, merry.Wrap(err)
		}
		if exc, ok := item["exclude"]; ok {
			filter, err := parseConfigFilters(exc)
			if err != nil {
				return nil, merry.Wrap(err)
			}
			return ConfigChatFilterExclude{filter}, nil
		}

		only, ook := item["only"]
		with, wok := item["with"]
		if ook && wok {
			filterOnly, err := parseConfigFilters(only)
			if err != nil {
				return nil, merry.Wrap(err)
			}
			filterWith, err := parseConfigFilters(with)
			if err != nil {
				return nil, merry.Wrap(err)
			}
			return ConfigChatFilterOnly{Only: filterOnly, With: filterWith}, nil
		}

		attrs := ConfigChatFilterAttrs{}
		if err := json.Unmarshal(buf, &attrs); err != nil {
			return nil, merry.Wrap(err)
		}
		return attrs, nil
	}

	return nil, merry.New("unexpected config item: " + string(buf))
}

func TraverseConfigChatFilter(root ConfigChatFilter, f func(ConfigChatFilter)) {
	f(root)
	switch specificRoot := root.(type) {
	case ConfigChatFilterMulti:
		for _, inner := range specificRoot.Inner {
			f(inner)
		}
	case ConfigChatFilterExclude:
		f(specificRoot.Inner)
	case ConfigChatFilterOnly:
		f(specificRoot.Only)
		f(specificRoot.With)
	}
}

func FindUnusedChatAttrsFilters(root ConfigChatFilter, chats []*Chat, f func(ConfigChatFilterAttrs)) {
	TraverseConfigChatFilter(root, func(filter ConfigChatFilter) {
		if attrs, ok := filter.(ConfigChatFilterAttrs); ok {
			found := false
			for _, chat := range chats {
				if attrs.Match(chat, nil) == MatchTrue {
					found = true
					break
				}
			}
			if !found {
				f(attrs)
			}
		}
	})
}

func CheckConfig(config *Config, chats []*Chat) {
	FindUnusedChatAttrsFilters(config.History, chats, func(attrs ConfigChatFilterAttrs) {
		log.Warn("no chats match history filter %v", attrs)
	})
	FindUnusedChatAttrsFilters(config.Media, chats, func(attrs ConfigChatFilterAttrs) {
		log.Warn("no chats match stories filter %v", attrs)
	})

	TraverseConfigChatFilter(config.History, func(filter ConfigChatFilter) {
		if attrs, ok := filter.(ConfigChatFilterAttrs); ok && attrs.MediaMaxSize != nil {
			log.Warn("'media_max_size' have no effect in 'config.history'")
		}
	})
	TraverseConfigChatFilter(config.Stories, func(filter ConfigChatFilter) {
		if attrs, ok := filter.(ConfigChatFilterAttrs); ok && attrs.MediaMaxSize != nil {
			log.Warn("'media_max_size' have no effect in 'config.stories'")
		}
	})
	for _, filter := range config.HistoryLimit {
		TraverseConfigChatFilter(filter, func(filter ConfigChatFilter) {
			if attrs, ok := filter.(ConfigChatFilterAttrs); ok && attrs.MediaMaxSize != nil {
				log.Warn("'media_max_size' have no effect in 'config.history_limit'")
			}
		})
	}
}
