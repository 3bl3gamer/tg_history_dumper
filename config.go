package main

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/ansel1/merry"
)

var defaultConfig = &Config{
	History:           ConfigChatFilterType{Type: ChatUser},
	Media:             ConfigChatFilterNone{},
	RequestIntervalMS: 1000,
	SessionFilePath:   "tg.session",
	OutDirPath:        "history",
}

type Config struct {
	AppID             int32
	AppHash           string
	History           ConfigChatFilter
	Media             ConfigChatFilter
	RequestIntervalMS int64
	SessionFilePath   string
	OutDirPath        string
}

type MatchResult int8

const (
	MatchTrue MatchResult = iota - 1
	MatchUndefined
	MatchFalse
)

type ConfigChatFilter interface {
	Match(*Chat) MatchResult
}

type ConfigChatFilterNone struct{}

func (f ConfigChatFilterNone) Match(chat *Chat) MatchResult { return MatchFalse }

type ConfigChatFilterAll struct{}

func (f ConfigChatFilterAll) Match(chat *Chat) MatchResult { return MatchTrue }

type ConfigChatFilterMulti struct {
	Inner []ConfigChatFilter
}

func (f ConfigChatFilterMulti) Match(chat *Chat) MatchResult {
	res := MatchUndefined
	for _, innerF := range f.Inner {
		m := innerF.Match(chat)
		if m != MatchUndefined {
			res = m
		}
	}
	return res
}

type ConfigChatFilterExclude struct {
	Inner ConfigChatFilter
}

func (f ConfigChatFilterExclude) Match(chat *Chat) MatchResult {
	if f.Inner.Match(chat) == MatchTrue {
		return MatchFalse
	}
	return MatchUndefined
}

type ConfigChatFilterType struct{ Type ChatType }

func (f ConfigChatFilterType) Match(chat *Chat) MatchResult {
	if chat.Type == f.Type {
		return MatchTrue
	}
	return MatchUndefined
}

type ConfigChatFilterAttrs struct {
	ID       *int32    `json:"id,omitempty"`
	Title    *string   `json:"title,omitempty"`
	Username *string   `json:"username,omitempty"`
	Type     *ChatType `json:"type,omitempty"`
}

func (f ConfigChatFilterAttrs) Match(chat *Chat) MatchResult {
	m := (f.ID == nil || chat.ID == *f.ID) &&
		(f.Title == nil || chat.Title == *f.Title) &&
		(f.Username == nil || chat.Username == *f.Username) &&
		(f.Type == nil || chat.Type == *f.Type)
	if m {
		return MatchTrue
	}
	return MatchUndefined
}

func (f ConfigChatFilterAttrs) String() string {
	buf, _ := json.Marshal(f)
	return string(buf)
}

type ConfigRaw struct {
	AppID             int32           `json:"app_id"`
	AppHash           string          `json:"app_hash"`
	History           json.RawMessage `json:"history"`
	Media             json.RawMessage `json:"media"`
	RequestIntervalMS int64           `json:"request_interval_ms"`
	SessionFilePath   string          `json:"session_file_path"`
	OutDirPath        string          `json:"out_dir_path"`
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

	cfg := &Config{
		AppID:   raw.AppID,
		AppHash: raw.AppHash,
	}

	cfg.RequestIntervalMS = defaultConfig.RequestIntervalMS
	if raw.RequestIntervalMS > 0 {
		cfg.RequestIntervalMS = raw.RequestIntervalMS
	}

	cfg.SessionFilePath = defaultConfig.SessionFilePath
	if raw.SessionFilePath != "" {
		cfg.SessionFilePath = raw.SessionFilePath
	}

	cfg.OutDirPath = defaultConfig.OutDirPath
	if raw.OutDirPath != "" {
		cfg.OutDirPath = raw.OutDirPath
	}

	cfg.History = defaultConfig.History
	if len(raw.History) > 0 {
		cfg.History, err = parseConfigFilters(raw.History)
		if err != nil {
			return nil, merry.Wrap(err)
		}
	}

	cfg.Media = defaultConfig.Media
	if len(raw.Media) > 0 {
		cfg.Media, err = parseConfigFilters(raw.Media)
		if err != nil {
			return nil, merry.Wrap(err)
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
	}
}

func FindUnusedChatAttrsFilters(root ConfigChatFilter, chats []*Chat, f func(ConfigChatFilterAttrs)) {
	TraverseConfigChatFilter(root, func(filter ConfigChatFilter) {
		if attrs, ok := filter.(ConfigChatFilterAttrs); ok {
			found := false
			for _, chat := range chats {
				if attrs.Match(chat) == MatchTrue {
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
