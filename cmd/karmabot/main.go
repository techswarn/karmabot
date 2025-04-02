package main

import (
	"flag"
	"strings"

	"github.com/kamaln7/karmabot"
	"github.com/kamaln7/karmabot/database"
	karmabotui "github.com/kamaln7/karmabot/ui"
	"github.com/kamaln7/karmabot/ui/blankui"
	"github.com/kamaln7/karmabot/ui/webui"
	"github.com/aybabtme/log"
	"github.com/kamaln7/envy"

	_ "github.com/joho/godotenv"
    "github.com/slack-go/slack"
    "github.com/slack-go/slack/socketmode"
	//"os"
	"fmt"
)

// cli flags
var (
	bottoken         = flag.String("bottoken", "", "Bot token")
	apptoken         = flag.String("apptoken", "", "App token")
	dbpath           = flag.String("db", "./db.sqlite3", "path to sqlite database")
	maxpoints        = flag.Int("maxpoints", 6, "the maximum amount of points that users can give/take at once")
	leaderboardlimit = flag.Int("leaderboardlimit", 10, "the default amount of users to list in the leaderboard")
	debug            = flag.Bool("debug", false, "set debug mode")
	webuitotp        = flag.String("webui.totp", "", "totp key")
	webuipath        = flag.String("webui.path", "", "path to web UI files")
	webuilistenaddr  = flag.String("webui.listenaddr", "", "address to listen and serve the web ui on")
	webuiurl         = flag.String("webui.url", "", "url address for accessing the web ui")
	motivate         = flag.Bool("motivate", true, "toggle motivate.im support")
	blacklist        = make(karmabot.StringList, 0)
	reactji          = flag.Bool("reactji", false, "use reactji as karma operations")
	upvotereactji    = make(karmabot.StringList, 0)
	downvotereactji  = make(karmabot.StringList, 0)
	aliases          = make(karmabot.StringList, 0)
	selfkarma        = flag.Bool("selfkarma", true, "allow users to add/remove karma to themselves")
	replytype        = flag.String("replytype", "message", "how to reply to commands (message, thread)")
	socketdebug	     = flag.Bool("socketdebug", true, "set socketmode debug mode")
)

func main() {
	// logging

	ll := log.KV("version", karmabot.Version)

	// cli flags

	flag.Var(&blacklist, "blacklist", "blacklist users from having karma operations applied on them")
	flag.Var(&aliases, "alias", "alias different users to one user")
	flag.Var(&upvotereactji, "reactji.upvote", "a list of reactjis to use for upvotes")
	flag.Var(&downvotereactji, "reactji.downvote", "a list of reactjis to use for downvotes")

	envy.Parse("KB")
	flag.Parse()

	// startup

	ll.Info("starting karmabot")

	// reactjis

	// reactji defaults
	if len(upvotereactji) == 0 {
		upvotereactji.Set("+1")
		upvotereactji.Set("thumbsup")
		upvotereactji.Set("thumbsup_all")
	}
	if len(downvotereactji) == 0 {
		downvotereactji.Set("-1")
		downvotereactji.Set("thumbsdown")
	}
	reactjiConfig := &karmabot.ReactjiConfig{
		Enabled:  *reactji,
		Upvote:   upvotereactji,
		Downvote: downvotereactji,
	}

	// format aliases
	aliasMap := make(karmabot.UserAliases, 0)
	for k := range aliases {
		users := strings.Split(k, "++")
		if len(users) <= 1 {
			ll.Fatal("invalid alias format. see documentation")
		}

		user := users[0]
		for _, alias := range users[1:] {
			aliasMap[alias] = user
		}
	}

	// database

	db, err := database.New(&database.Config{
		Path: *dbpath,
	})

	if err != nil {
		ll.KV("path", *dbpath).Err(err).Fatal("could not open sqlite db")
	}

	// Adding new code here
    // godotenv.Load(".env")
 
    // token := os.Getenv("SLACK_AUTH_TOKEN")
    // appToken := os.Getenv("SLACK_APP_TOKEN")
	fmt.Println("bottoken: ", *bottoken)
	fmt.Println("apptoken: ", *apptoken)
	
	if *bottoken == "" {
		ll.Fatal("please pass the slack Bot token (see `karmabot -h` for help")
	}

	if *apptoken == "" {
		ll.Fatal("please pass the slack App token (see `karmabot -h` for help")
	}
 
    client := slack.New(*bottoken, slack.OptionDebug(*socketdebug), slack.OptionAppLevelToken(*apptoken))
 
    socketClient := socketmode.New(
        client,
        socketmode.OptionDebug(*socketdebug),
	)

	var ui karmabotui.Provider
	if *webuipath != "" && *webuilistenaddr != "" {
		ui, err = webui.New(&webui.Config{
			ListenAddr:       *webuilistenaddr,
			URL:              *webuiurl,
			FilesPath:        *webuipath,
			TOTP:             *webuitotp,
			LeaderboardLimit: *leaderboardlimit,
			Log:              ll.KV("provider", "webui"),
			Debug:            *debug,
			DB:               db,
		})

		if err != nil {
			ll.Err(err).Fatal("could not initialize web ui")
		}
	} else {
		ui = blankui.New()
	}
	go ui.Listen()

	bot := karmabot.NewBot(&karmabot.Config{
		Slack: &karmabot.SlackChatService{
			Client: *socketClient,
			API: client,
		},
		UI:               ui,
		Debug:            *debug,
		MaxPoints:        *maxpoints,
		LeaderboardLimit: *leaderboardlimit,
		Log:              ll,
		DB:               db,
		UserBlacklist:    blacklist,
		Reactji:          reactjiConfig,
		Motivate:         *motivate,
		Aliases:          aliasMap,
		SelfKarma:        *selfkarma,
		ReplyType:        *replytype,
	})

	go bot.Listen()

	socketClient.Run()
}
