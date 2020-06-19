package main

import (
	"flag"
	"os"
	"time"

	"github.com/3bl3gamer/tgclient"
	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry"
)

type LogHandler struct {
	mtproto.SimpleLogHandler
}

func (h LogHandler) Log(level mtproto.LogLevel, err error, msg string, args ...interface{}) {
	if level != mtproto.DEBUG {
		h.SimpleLogHandler.Log(level, err, msg, args...)
	}
}

func (h LogHandler) Message(isIncoming bool, msg mtproto.TL, id int64) {
	h.Log(mtproto.DEBUG, nil, h.StringifyMessage(isIncoming, msg, id))
}

type FileProgressLogger struct {
	prevProgress int64
}

func (l *FileProgressLogger) OnProgress(fileLocation mtproto.TL, offset, size int64) {
	prog := offset * 100 / size
	if prog == 100 && l.prevProgress == 0 {
		return //got file in one step, no need to log it
	}
	if prog == 100 || prog-l.prevProgress >= 5 {
		log.Info("%d%%", prog)
		l.prevProgress = prog
	}
}

var tgLogHandler = &LogHandler{}
var log = mtproto.Logger{Hnd: tgLogHandler}

func loadAndSaveMessages(tg *tgclient.TGClient, chat *Chat, saver HistorySaver, config *Config) error {
	lastID, err := saver.GetLastMessageID(chat)
	if err != nil {
		return merry.Wrap(err)
	}
	startID := lastID
	limit := int32(100)

	prevIterTime := time.Now()
	for {
		if lastID >= chat.LastMessageID {
			break
		}

		percent := (lastID - startID) * 100 / (chat.LastMessageID - startID)
		log.Info("loading messages: \033[32m%d%%\033[0m from #%d (+%d) until #%d",
			percent, lastID, limit, chat.LastMessageID)

		allMessages, users, err := tgLoadMessages(tg, chat.Obj, limit, lastID)
		if err != nil {
			return merry.Wrap(err)
		}

		newMessages := make([]mtproto.TL, 0, len(allMessages))
		for _, msg := range allMessages {
			msgID, err := tgGetMessageID(msg)
			if err != nil {
				return merry.Wrap(err)
			}
			newMessages = append(newMessages, msg)
			if msgID > lastID {
				lastID = msgID
				if startID == 0 {
					startID = lastID
				}
			}
		}
		log.Debug("got %d new message(s)", len(newMessages))

		if err := saver.SaveSenders(users); err != nil {
			return merry.Wrap(err)
		}

		if err := saver.SaveMessages(chat, newMessages); err != nil {
			return merry.Wrap(err)
		}

		now := time.Now()
		delta := time.Duration(config.RequestIntervalMS)*time.Millisecond - now.Sub(prevIterTime)
		time.Sleep(delta)
		prevIterTime = now
	}
	return nil
}

func dump() error {
	appID := flag.Int("app-id", 0, "app id")
	appHash := flag.String("app-hash", "", "app hash")
	sessionFPath := flag.String("session", "", "session file path")
	outDirPath := flag.String("out", "", "output directory path")
	configFPath := flag.String("config", "config.json", "path to config file")
	chatTitle := flag.String("chat", "", "name of the chat to dump")
	doListChats := flag.Bool("list-chats", false, "list all available chats")
	flag.Parse()

	config, err := ParseConfig(*configFPath)
	if err != nil {
		return merry.Wrap(err)
	}
	if *appID != 0 {
		config.AppID = int32(*appID)
	}
	if *appHash != "" {
		config.AppHash = *appHash
	}
	if *chatTitle != "" {
		config.History = ConfigChatFilterAttrs{Title: chatTitle}
	}
	if *sessionFPath != "" {
		config.SessionFilePath = *sessionFPath
	}
	if *outDirPath != "" {
		config.OutDirPath = *outDirPath
	}

	if config.AppID == 0 || config.AppHash == "" {
		println("app_id and app_hash are required (in config or flags)")
		flag.Usage()
		os.Exit(2)
	}

	tg, err := tgConnect(config.AppID, config.AppHash, config.SessionFilePath)
	if err != nil {
		return merry.Wrap(err)
	}

	saver := &JSONFilesHistorySaver{Dirpath: config.OutDirPath}
	saver.SetFileRequestCallback(func(chat *Chat, file *TGFileInfo, fpath string) error {
		var err error
		if config.Media.Match(chat) == MatchTrue {
			_, err = os.Stat(fpath)
			if os.IsNotExist(err) {
				log.Info("downloading file to %s", fpath)
				_, err = tg.DownloadFileToPath(fpath, file.InputLocation, file.DcID, int64(file.Size), &FileProgressLogger{})
			}
		}
		return merry.Wrap(err)
	})

	chats, err := tgLoadChats(tg)
	if err != nil {
		return merry.Wrap(err)
	}

	FindUnusedChatAttrsFilters(config.History, chats, func(attrs ConfigChatFilterAttrs) {
		log.Warn("no chats match filter %v", attrs)
	})

	if *doListChats {
		for _, chat := range chats {
			format := "%-7s %10d \033[32m%s\033[0m (%s)"
			if config.History.Match(chat) != MatchTrue {
				format = "\033[90m%-7s %10d %s (%s)\033[0m"
			}
			log.Info(format, chat.Type, chat.ID, chat.Title, chat.Username)
		}
	} else {
		for _, chat := range chats {
			if config.History.Match(chat) == MatchTrue {
				log.Info("saving messages from: \033[32m%s\033[0m (%s) #%d %v",
					chat.Title, chat.Username, chat.ID, chat.Type)
				if err := loadAndSaveMessages(tg, chat, saver, config); err != nil {
					return merry.Wrap(err)
				}
			}
		}
	}
	return nil
}

func main() {
	if err := dump(); err != nil {
		panic(merry.Details(err))
	}
}
