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
		RequestIntervalMS: int64(1000),
		History:           ConfigChatFilterType{Type: ChatUser},
		Media:             ConfigChatFilterNone{},
		DoAccountDump:     "off",
		DoContactsDump:    "off",
		DoSessionsDump:    "off",
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
		RequestIntervalMS: int64(1000),
		History:           ConfigChatFilterType{Type: ChatUser},
		Media:             ConfigChatFilterNone{},
		DoAccountDump:     "off",
		DoContactsDump:    "off",
		DoSessionsDump:    "off",
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
			]},
			{"only": {"type": "user"}, "with": {"id": 123}}
		],
		"dump_account": "off",
		"dump_contacts": "yes"
	}`)
	defer removeTestConfig(file)
	assertOk(t, err)

	cfg, err := ParseConfig(file.Name())
	assertOk(t, err)
	id123 := int32(123)
	bla := "bla"
	uname := "uname"
	userType := ChatUser
	channelType := ChatChannel
	assertEqual(t, cfg, &Config{
		OutDirPath:        "out",
		SessionFilePath:   "sessfile",
		RequestIntervalMS: 500,
		History: ConfigChatFilterMulti{Inner: []ConfigChatFilter{
			ConfigChatFilterNone{},
			ConfigChatFilterAttrs{ID: &id123},
			ConfigChatFilterExclude{Inner: ConfigChatFilterMulti{Inner: []ConfigChatFilter{
				ConfigChatFilterAll{},
				ConfigChatFilterAttrs{Title: &bla, Username: &uname},
				ConfigChatFilterAttrs{Type: &channelType},
			}}},
			ConfigChatFilterOnly{
				Only: ConfigChatFilterAttrs{Type: &userType},
				With: ConfigChatFilterAttrs{ID: &id123},
			},
		}},
		Media:          ConfigChatFilterNone{},
		DoAccountDump:  "off",
		DoContactsDump: "yes",
		DoSessionsDump: "off",
	})
}

func TestConfigChatFilter(t *testing.T) {
	var f ConfigChatFilter
	id123 := int32(123)
	bla := "bla"
	uname := "uname"
	channelType := ChatChannel

	// overrides
	f = ConfigChatFilterMulti{[]ConfigChatFilter{
		ConfigChatFilterNone{},
		ConfigChatFilterAttrs{ID: &id123},
		ConfigChatFilterExclude{ConfigChatFilterAttrs{Title: &bla, Username: &uname}},
		ConfigChatFilterAttrs{Type: &channelType},
	}}
	assertEqual(t, f.Match(&Chat{ID: 123}, nil), MatchTrue)
	assertEqual(t, f.Match(&Chat{Type: ChatChannel}, nil), MatchTrue)
	assertEqual(t, f.Match(&Chat{ID: 123, Title: "bla", Username: "not-uname"}, nil), MatchTrue)
	assertEqual(t, f.Match(&Chat{ID: 123, Title: "bla", Username: "uname"}, nil), MatchFalse)
	assertEqual(t, f.Match(&Chat{ID: 123, Title: "bla", Username: "uname", Type: ChatChannel}, nil), MatchTrue)
	assertEqual(t, f.Match(&Chat{Title: "no-match"}, nil), MatchFalse)

	// no match
	f = ConfigChatFilterMulti{[]ConfigChatFilter{
		ConfigChatFilterAll{},
		ConfigChatFilterAttrs{ID: &id123},
	}}
	assertEqual(t, f.Match(&Chat{Title: "no-match"}, nil), MatchTrue)

	// only-filter
	f = ConfigChatFilterOnly{
		Only: ConfigChatFilterAttrs{Type: &channelType},
		With: ConfigChatFilterAttrs{ID: &id123},
	}
	assertEqual(t, f.Match(&Chat{ID: 123, Type: ChatChannel}, nil), MatchTrue)
	assertEqual(t, f.Match(&Chat{ID: 123, Type: ChatUser}, nil), MatchUndefined)
	assertEqual(t, f.Match(&Chat{ID: 12, Type: ChatChannel}, nil), MatchUndefined)

	// files
	size512K := SuffuxedSize(512 * 1024)
	f = ConfigChatFilterAttrs{ID: &id123, MediaMaxSize: &size512K}
	assertEqual(t, f.Match(&Chat{ID: 123}, nil), MatchTrue)
	assertEqual(t, f.Match(&Chat{ID: 123}, &TGFileInfo{Size: 512 * 1024}), MatchTrue)
	assertEqual(t, f.Match(&Chat{ID: 123}, &TGFileInfo{Size: 512*1024 + 1}), MatchUndefined)
	assertEqual(t, f.Match(&Chat{ID: 12}, &TGFileInfo{Size: 512 * 1024}), MatchUndefined)
}

func Test_ConfigChatHistoryLimit_For(t *testing.T) {
	id1 := int32(1)
	id2 := int32(2)

	var l ConfigChatHistoryLimit = map[int32]ConfigChatFilter{
		1000: ConfigChatFilterAttrs{ID: &id1},
		2000: ConfigChatFilterMulti{[]ConfigChatFilter{
			ConfigChatFilterAttrs{ID: &id1},
			ConfigChatFilterAttrs{ID: &id2},
		}},
	}
	assertEqual(t, l.For(&Chat{ID: 1}), int32(1000))
	assertEqual(t, l.For(&Chat{ID: 2}), int32(2000))
	assertEqual(t, l.For(&Chat{ID: 3}), int32(0))
}
