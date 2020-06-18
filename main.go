package main

import (
	"flag"
	"os"
	"time"

	"github.com/3bl3gamer/tgclient"
	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry"
)

type ChatType int8

const (
	ChatUser ChatType = iota
	ChatGroup
	ChatChannel
)

type Chat struct {
	ID            int32
	Title         string
	Username      string
	LastMessageID int32
	Type          ChatType
	Obj           mtproto.TL
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

type LogHandler struct {
	mtproto.SimpleLogHandler
}

func (h LogHandler) Log(level mtproto.LogLevel, err error, msg string, args ...interface{}) {
	if level != mtproto.DEBUG {
		h.SimpleLogHandler.Log(level, err, msg, args...)
	}
	//h.AddLevelPrevix(level, h.StringifyLog(level, err, msg, args...))
}

func (h LogHandler) Message(isIncoming bool, msg mtproto.TL, id int64) {
	h.Log(mtproto.DEBUG, nil, h.StringifyMessage(isIncoming, msg, id))
}

var tgLogHandler = &LogHandler{}
var log = mtproto.Logger{Hnd: tgLogHandler}

func loadAndSaveMessages(tg *tgclient.TGClient, chat *Chat, saver HistorySaver) error {
	lastID, err := saver.GetLastMessageID(chat)
	if err != nil {
		return merry.Wrap(err)
	}
	startID := lastID
	limit := int32(100)

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

		// for i, msg := range newMessages {
		// 	println(" ---=====--- ")
		// 	fmt.Printf("%d === %#v\n", i, msg)
		// 	fmt.Println(tgObjToMap(msg))
		// 	buf, err := json.Marshal(tgObjToMap(msg))
		// 	if err != nil {
		// 		return merry.Wrap(err)
		// 	}
		// 	println(string(buf))
		// }
		if err := saver.SaveMessages(chat, newMessages); err != nil {
			return merry.Wrap(err)
		}
		time.Sleep(time.Second / 2)
	}
	return nil
}

func dump() error {
	appID := flag.Int("app_id", 0, "app id")
	appHash := flag.String("app_hash", "", "app hash")
	sessionFName := flag.String("session", "tg.session", "session file path")
	outDirPath := flag.String("out", "json", "output directory path")
	chatName := flag.String("chat", "", "name of the chat to dump")
	flag.Parse()

	if *appID == 0 || *appHash == "" {
		println("App ID and hash are required!")
		flag.Usage()
		os.Exit(2)
	}

	tg, err := tgConnect(*appID, *appHash, *sessionFName)
	if err != nil {
		return merry.Wrap(err)
	}

	saver := &JSONFilesHistorySaver{Dirpath: *outDirPath}
	// saver.SetFileRequestCallback(func(file *TGFileInfo, fpath string) error {
	// 	_, err := os.Stat(fpath)
	// 	if os.IsNotExist(err) {
	// 		log.Info("downloading file to %s", fpath)
	// 		_, err = tg.DownloadFileToPath(fpath, file.InputLocation, file.DcID, int64(file.Size), &FileProgressLogger{})
	// 	}
	// 	return merry.Wrap(err)
	// })

	chats, err := tgLoadChats(tg)
	if err != nil {
		return merry.Wrap(err)
	}
	for _, d := range chats {
		if d.Title == *chatName {
			log.Info("saving messages from: \033[32m%s (%s)\033[0m #%d %T", d.Title, d.Username, d.ID, d.Obj)
			if err := loadAndSaveMessages(tg, d, saver); err != nil {
				return merry.Wrap(err)
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
