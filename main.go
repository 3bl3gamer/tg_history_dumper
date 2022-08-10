package main

import (
	"flag"
	"fmt"
	stdlog "log"
	"os"
	"path/filepath"
	"time"

	"github.com/3bl3gamer/tgclient"
	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry"
	"github.com/fatih/color"
)

type LogHandler struct {
	mtproto.ColorLogHandler
	ConsoleMaxLevel mtproto.LogLevel
	ErrorFileLoger  *stdlog.Logger
	DebugFileLoger  *stdlog.Logger
	ConsoleLogger   *stdlog.Logger
}

func (h LogHandler) Log(level mtproto.LogLevel, err error, msg string, args ...interface{}) {
	text := h.StringifyLog(level, err, msg, args...)
	text = h.AddLevelPrevix(level, text)
	if level <= h.ConsoleMaxLevel {
		h.ConsoleLogger.Print(h.AddLevelColor(level, text))
	}
	if level <= mtproto.ERROR {
		h.ErrorFileLoger.Print(text)
	}
	h.DebugFileLoger.Print(text)
}

func (h LogHandler) Message(isIncoming bool, msg mtproto.TL, id int64) {
	h.Log(mtproto.DEBUG, nil, h.StringifyMessage(isIncoming, msg, id))
}

type FileProgressLogger struct {
	prevProgress int64
	prevTime     time.Time
}

func NewFileProgressLogger() *FileProgressLogger {
	return &FileProgressLogger{prevTime: time.Now()}
}

func (l *FileProgressLogger) OnProgress(fileLocation mtproto.TL, offset, size int64) {
	prog := offset * 100 / size
	if prog == 100 && l.prevProgress == 0 {
		return //got file in one step, no need to log it
	}
	if prog == 100 || time.Now().Sub(l.prevTime) > 2*time.Second {
		log.Info("%d%%", prog)
		l.prevProgress = prog
		l.prevTime = time.Now()
	}
}

var log mtproto.Logger

func saveChatsAsRelated(chats []*Chat, saver HistorySaver) error {
	var users, groupsAndChannels []mtproto.TL
	for _, c := range chats {
		if _, ok := c.Obj.(mtproto.TL_user); ok {
			users = append(users, c.Obj)
		} else {
			groupsAndChannels = append(groupsAndChannels, c.Obj)
		}
	}
	if err := saver.SaveRelatedUsers(users); err != nil {
		return merry.Wrap(err)
	}
	if err := saver.SaveRelatedChats(groupsAndChannels); err != nil {
		return merry.Wrap(err)
	}
	return nil
}

func loadAndSaveMessages(tg *tgclient.TGClient, chat *Chat, saver HistorySaver, config *Config) error {
	lastID, err := saver.GetLastMessageID(chat)
	if err != nil {
		return merry.Wrap(err)
	}
	startID := lastID
	chunkSize := int32(100)

	historyLimit := int32(0)
	// applying limit only if no history has been dumped for this chat yet
	if startID == 0 {
		historyLimit = config.HistoryLimit.For(chat)
	}
	if historyLimit > chat.LastMessageID {
		log.Debug("history limit is set to %d, but chat has %d message(s) as most (it is the last message ID), disabling limit",
			historyLimit, chat.LastMessageID)
		historyLimit = 0
	}

	greenf := color.New(color.FgGreen).SprintfFunc()

	prevIterTime := time.Now()
	for {
		if lastID >= chat.LastMessageID {
			break
		}

		{
			percent := (lastID - startID) * 100 / (chat.LastMessageID - startID)
			approxRemCount := chat.LastMessageID - lastID
			fromNum := lastID
			limitText := ""
			if historyLimit > 0 {
				if historyLimit < approxRemCount {
					approxRemCount = historyLimit
				}
				fromNum = -historyLimit
				if fromNum < 0 {
					fromNum = 0
				}
				limitText = fmt.Sprintf(", %d limit", historyLimit)
			}
			log.Info("loading messages: %s from #%d (+%d) until #%d (~%d left%s)",
				greenf("%d%%", percent), fromNum, chunkSize, chat.LastMessageID, approxRemCount, limitText)
		}

		newMessages, users, chats, err := tgLoadMessages(tg, chat.Obj, chunkSize, lastID, historyLimit)
		if err != nil {
			return merry.Wrap(err)
		}
		if len(newMessages) == 0 && historyLimit > 0 {
			// historyLimit applies a negative offset and (if a lot of messages were removed from the begining of the chat)
			// returned message chunk may be empty. That means, there are less than `historyLimit-chunkSize` remaining messages,
			// so we can just disable limit and retry
			log.Debug("got no messages for %d offset; looks like there are less than %d messages, starting from the first one",
				-historyLimit, historyLimit-chunkSize)
			historyLimit = 0
		} else {
			// using limit only once: when it is positive, reference messages chunk will be loaded
			// (with approximatelly chunk_first_msg_ID = last_msg_ID_in_this_chat - historyLimit)
			// and subsequent loading will go on as usual (from older messages to newer ones)
			historyLimit = 0

			if err := saver.SaveRelatedUsers(users); err != nil {
				return merry.Wrap(err)
			}

			if err := saver.SaveRelatedChats(chats); err != nil {
				return merry.Wrap(err)
			}

			for _, msg := range newMessages {
				msgID, err := tgGetMessageID(msg)
				if err != nil {
					return merry.Wrap(err)
				}
				if msgID > lastID {
					lastID = msgID
				}
				if msgID < startID || startID == 0 {
					startID = msgID
				}
			}
			log.Debug("got %d new message(s)", len(newMessages))

			if err := saver.SaveMessages(chat, newMessages); err != nil {
				return merry.Wrap(err)
			}

			if len(newMessages) < int(chunkSize) && lastID < chat.LastMessageID {
				log.Warn(
					"go %d message(s) (instead of %d), but their last ID=%d is still less than chat last message ID=%d; "+
						"maybe someone has removed last message(s) while we were dumping; stopping with this chat for now.",
					len(newMessages), chunkSize, lastID, chat.LastMessageID)
				break
			}
		}

		now := time.Now()
		delta := time.Duration(config.RequestIntervalMS)*time.Millisecond - now.Sub(prevIterTime)
		time.Sleep(delta)
		prevIterTime = now
	}
	return nil
}

func mustOpen(fpath string) *os.File {
	file, err := os.OpenFile(fpath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	return file
}

func isBrokenFileError(err error) bool {
	return err != nil && err.Error() == `unexpected file part: mtproto.TL_rpc_error{ErrorCode:400, ErrorMessage:"LOCATION_INVALID"}`
}

func dump() error {
	// flags
	configFPath := flag.String("config", "config.json", "path to config file")
	appID := flag.Int("app-id", 0, "app id")
	appHash := flag.String("app-hash", "", "app hash")
	sosks5addr := flag.String("socks5", "", "socks5 proxy address:port, overrides config.socks5_proxy_addr")
	sosks5user := flag.String("socks5-user", "", "socks5 proxy username, overrides config.socks5_proxy_user")
	sosks5password := flag.String("socks5-password", "", "socks5 proxy password, overrides config.socks5_proxy_password")
	sessionFPath := flag.String("session", "", "session file path, overrides config.session_file_path")
	outDirPath := flag.String("out", "", "output directory path, overriders config.out_dir_path")
	chatTitle := flag.String("chat", "", "title of the chat to dump, overrides config.history")
	doListChats := flag.Bool("list-chats", false, "list all available chats")
	doLogout := flag.Bool("logout", false, "Logout and clear session")
	logDebug := flag.Bool("debug", false, "show debug log messages")
	tgLogDebug := flag.Bool("debug-tg", false, "show debug TGClient log messages")
	doAccountDump := flag.String("dump-account", "", "enable basic user information dump, use 'write' to enable dump, overriders config.dump_account")
	doContactsDump := flag.String("dump-contacts", "", "enable contacts dump, use 'write' to enable dump, overriders config.dump_contacts")
	doSessionsDump := flag.String("dump-sessions", "", "enable active sessions dump, use 'write' to enable dump, overriders config.dump_sessions")
	flag.Parse()

	// logging
	executablePath, _ := os.Executable()
	executableDir := filepath.Dir(executablePath)
	commonLogHandler := LogHandler{
		ConsoleMaxLevel: mtproto.INFO,
		DebugFileLoger:  stdlog.New(mustOpen(filepath.Join(executableDir, "debug.log")), "", stdlog.LstdFlags),
		ErrorFileLoger:  stdlog.New(mustOpen(filepath.Join(executableDir, "error.log")), "", stdlog.LstdFlags),
		ConsoleLogger:   stdlog.New(color.Error, "", stdlog.LstdFlags),
	}
	tgLogHandler := commonLogHandler
	if *logDebug {
		commonLogHandler.ConsoleMaxLevel = mtproto.DEBUG
	}
	if *tgLogDebug {
		tgLogHandler.ConsoleMaxLevel = mtproto.DEBUG
	}
	log = mtproto.Logger{Hnd: commonLogHandler}

	// separating from older log
	for _, logger := range []*stdlog.Logger{commonLogHandler.DebugFileLoger, commonLogHandler.ErrorFileLoger} {
		logger.Print("")
		logger.Print("")
		logger.Print(" === HISTORY DUMP START ===")
		logger.Print("")
		logger.Print("")
	}

	// config
	config, err := ParseConfig(*configFPath)
	if err != nil {
		return merry.Wrap(err)
	}
	overrideStrParam := func(cfgAttr, srcValue *string) {
		if *srcValue != "" {
			*cfgAttr = *srcValue
		}
	}
	if *appID != 0 {
		config.AppID = int32(*appID)
	}
	overrideStrParam(&config.AppHash, appHash)
	overrideStrParam(&config.Socks5ProxyAddr, sosks5addr)
	overrideStrParam(&config.Socks5ProxyUser, sosks5user)
	overrideStrParam(&config.Socks5ProxyPassword, sosks5password)
	if *chatTitle != "" {
		config.History = ConfigChatFilterAttrs{Title: chatTitle}
	}
	overrideStrParam(&config.SessionFilePath, sessionFPath)
	overrideStrParam(&config.OutDirPath, outDirPath)
	overrideStrParam(&config.DoAccountDump, doAccountDump)
	overrideStrParam(&config.DoContactsDump, doContactsDump)
	overrideStrParam(&config.DoSessionsDump, doSessionsDump)

	if config.AppID == 0 || config.AppHash == "" {
		println("app_id and app_hash are required (in config or flags)")
		flag.Usage()
		os.Exit(2)
	}

	// tg setup
	tg, me, err := tgConnect(config, &tgLogHandler)
	if err != nil {
		return merry.Wrap(err)
	}

	saver := &JSONFilesHistorySaver{Dirpath: config.OutDirPath}
	saver.SetFileRequestCallback(func(chat *Chat, file *TGFileInfo, msgID int32) error {
		var err error
		if config.Media.Match(chat, file) == MatchTrue {
			fpath, err := saver.MessageFileFPath(chat, msgID, file.FName)
			_, err = os.Stat(fpath)
			if os.IsNotExist(err) {
				log.Info("downloading file to %s", fpath)
				_, err = tg.DownloadFileToPath(fpath, file.InputLocation, file.DcID, int64(file.Size), NewFileProgressLogger())
				if isBrokenFileError(err) {
					log.Error(nil, "in chat %d %s (%s): wrong file: %s", chat.ID, chat.Title, chat.Username, fpath)
					err = nil
				}
			}
		} else {
			log.Debug("skipping file '%s' of message #%d", file.FName, msgID)
		}
		return merry.Wrap(err)
	})

	// loading chats
	chats, err := tgLoadChats(tg)
	if err != nil {
		return merry.Wrap(err)
	}

	CheckConfig(config, chats)

	// processing chats
	if *doListChats {
		green := color.New(color.FgGreen).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()
		grayf := color.New(color.FgHiBlack).SprintfFunc()
		noopf := color.New().SprintfFunc()
		log.Info(grayf(" type     chat ID    limit  title (username)"))
		for _, chat := range chats {
			colf := noopf
			title := chat.Title
			historyLimitStr := "       "
			if historyLimit := config.HistoryLimit.For(chat); historyLimit != 0 {
				historyLimitStr = fmt.Sprintf("%7d", historyLimit)
			}
			if config.History.Match(chat, nil) == MatchTrue {
				title = green(title)
				historyLimitStr = yellow(historyLimitStr)
			} else {
				colf = grayf
			}
			log.Info(colf("%-7s %10d %s  %s (%s)", chat.Type, chat.ID, historyLimitStr, title, chat.Username))
		}

	}else if (*doLogout){

		tgLogout(tg)

	} else {
		// saveing user info
		if config.DoAccountDump == "write" {
			saver := &JSONFilesHistorySaver{Dirpath: config.OutDirPath}
			saver.SaveAccount(*me)
			log.Info("User Account Info Saved")
		}

		// saveing contacts
		if config.DoContactsDump == "write" {
			contacts, err := tgLoadContacts(tg)
			if err != nil {
				return merry.Wrap(err)
			}
			saver.SaveContacts(contacts.Users)
			log.Info("Contacts Saved")
		}

		// saveing sessions
		if config.DoSessionsDump == "write" {
			authList, err := tgLoadAuths(tg)
			if err != nil {
				return merry.Wrap(err)
			}
			saver.SaveAuths(authList)
			log.Info("Active Sessions Saved")
		}

		// saving messages
		if err := saveChatsAsRelated(chats, saver); err != nil {
			return merry.Wrap(err)
		}
		green := color.New(color.FgGreen).SprintFunc()
		for _, chat := range chats {
			if config.History.Match(chat, nil) == MatchTrue {
				log.Info("saving messages from: %s (%s) #%d %v",
					green(chat.Title), chat.Username, chat.ID, chat.Type)
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
		log.Error(err, "")
		os.Exit(1)
	}
}
