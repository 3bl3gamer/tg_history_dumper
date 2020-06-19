package main

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/go-test/deep"
)

func writeTestConfig(cfg string) (*os.File, error) {
	file, err := ioutil.TempFile("", "cfg")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write([]byte(cfg)); err != nil {
		return nil, err
	}
	return file, nil
}

func removeTestConfig(file *os.File) {
	file.Close()
	os.Remove(file.Name())
}

func assertOk(t *testing.T, err error) {
	if err != nil {
		t.Fatal(err)
	}
}

func assertEqual(t *testing.T, a interface{}, b interface{}) {
	diff := deep.Equal(a, b)
	if diff != nil {
		t.Error(diff)
	}
}

func TestParseConfig__NoFile(t *testing.T) {
	silentParseTestMode = true
	cfg, err := ParseConfig("blablafile")
	assertOk(t, err)
	assertEqual(t, cfg, &Config{
		OutDirPath:        "history",
		SessionFilePath:   "tg.session",
		RequestIntervalMS: int64(800),
		History:           ConfigChatFilterType{Type: ChatUser},
		Media:             ConfigChatFilterNone{},
	})
}

func TestParseConfig__Empty(t *testing.T) {
	file, err := writeTestConfig(`{}`)
	defer removeTestConfig(file)
	assertOk(t, err)

	cfg, err := ParseConfig(file.Name())
	assertOk(t, err)
	assertEqual(t, cfg, &Config{
		OutDirPath:        "history",
		SessionFilePath:   "tg.session",
		RequestIntervalMS: int64(800),
		History:           ConfigChatFilterType{Type: ChatUser},
		Media:             ConfigChatFilterNone{},
	})
}

func TestParseConfig__Some(t *testing.T) {
	file, err := writeTestConfig(`{
		"out_dir_path": "out",
		"session_file_path": "sessfile",
		"request_interval_ms": 500,
		"history": [
			"none",
			{"id": 123},
			{"exclude": [
				"all",
				{"title": "bla", "username": "uname"},
				{"type": "channel"}
			]}
		]
	}`)
	defer removeTestConfig(file)
	assertOk(t, err)

	cfg, err := ParseConfig(file.Name())
	assertOk(t, err)
	id := int32(123)
	title := "bla"
	username := "uname"
	ctype := ChatChannel
	assertEqual(t, cfg, &Config{
		OutDirPath:        "out",
		SessionFilePath:   "sessfile",
		RequestIntervalMS: 500,
		History: ConfigChatFilterMulti{Inner: []ConfigChatFilter{
			ConfigChatFilterNone{},
			ConfigChatFilterAttrs{ID: &id},
			ConfigChatFilterExclude{Inner: ConfigChatFilterMulti{Inner: []ConfigChatFilter{
				ConfigChatFilterAll{},
				ConfigChatFilterAttrs{Title: &title, Username: &username},
				ConfigChatFilterAttrs{Type: &ctype},
			}}},
		}},
		Media: ConfigChatFilterNone{},
	})
}

func TestConfigChatFilter(t *testing.T) {
	id := int32(123)
	title := "bla"
	username := "uname"
	ctype := ChatChannel
	f := ConfigChatFilterMulti{[]ConfigChatFilter{
		ConfigChatFilterNone{},
		ConfigChatFilterAttrs{ID: &id},
		ConfigChatFilterExclude{ConfigChatFilterAttrs{Title: &title, Username: &username}},
		ConfigChatFilterAttrs{Type: &ctype},
	}}

	assertEqual(t, f.Match(&Chat{ID: 123}), MatchTrue)
	assertEqual(t, f.Match(&Chat{Type: ChatChannel}), MatchTrue)
	assertEqual(t, f.Match(&Chat{ID: 123, Title: "bla", Username: "not-uname"}), MatchTrue)
	assertEqual(t, f.Match(&Chat{ID: 123, Title: "bla", Username: "uname"}), MatchFalse)
	assertEqual(t, f.Match(&Chat{ID: 123, Title: "bla", Username: "uname", Type: ChatChannel}), MatchTrue)
	assertEqual(t, f.Match(&Chat{Title: "no-match"}), MatchFalse)

	f = ConfigChatFilterMulti{[]ConfigChatFilter{
		ConfigChatFilterAll{},
		ConfigChatFilterAttrs{ID: &id},
	}}
	assertEqual(t, f.Match(&Chat{Title: "no-match"}), MatchTrue)
}
