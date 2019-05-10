package main

import (
	"flag"
	"os"
	"time"

	"github.com/3bl3gamer/tgclient"
	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry"
)

type Dialog struct {
	ID            int32
	Title         string
	Username      string
	LastMessageID int32
	Obj           mtproto.TL
}

var tgLogHandler = &mtproto.SimpleLogHandler{}
var log = mtproto.Logger{Hnd: tgLogHandler}

func loadAndSaveMessages(tg *tgclient.TGClient, dialog *Dialog, saver HistorySaver) error {
	lastID, err := saver.GetLastMessageID(dialog)
	if err != nil {
		return merry.Wrap(err)
	}
	limit := int32(100)

	for {
		log.Info("loading messages from #%d (+%d) until #%d\n", lastID, limit, dialog.LastMessageID)

		allMessages, err := tgLoadChannelMessages(tg, dialog.Obj.(mtproto.TL_channel), limit, lastID)
		if err != nil {
			return merry.Wrap(err)
		}

		newMessages := make([]mtproto.TL, 0, len(allMessages))
		for _, msg := range allMessages {
			msgID, err := tgGetMessageID(msg)
			if err != nil {
				return merry.Wrap(err)
			}
			if msgID > lastID {
				newMessages = append(newMessages, msg)
			}
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
		if err := saver.SaveMessages(dialog, newMessages); err != nil {
			return merry.Wrap(err)
		}

		lastID += limit
		if lastID >= dialog.LastMessageID {
			break
		}
		time.Sleep(time.Second)
	}
	return nil
}

func dump() error {
	appID := flag.Int("app_id", 0, "app id")
	appHash := flag.String("app_hash", "", "app hash")
	flag.Parse()

	if *appID == 0 || *appHash == "" {
		println("App ID and hash are required!")
		flag.Usage()
		os.Exit(2)
	}

	tg, err := tgConnect(*appID, *appHash)
	if err != nil {
		return merry.Wrap(err)
	}

	saver := JSONFilesHistorySaver{"json"}

	dialogs, err := tgLoadDialogs(tg)
	if err != nil {
		return merry.Wrap(err)
	}
	for _, d := range dialogs {
		if d.Username == "contests" {
			log.Info("saving messages from: \033[32m%s (%s)\033[0m #%d %T\n", d.Title, d.Username, d.ID, d.Obj)
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
