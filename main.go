package karmabot

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kamaln7/karmabot/database"
	"github.com/kamaln7/karmabot/ui"
	"github.com/aybabtme/log"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/slack-go/slack/slackevents"
	"github.com/kamaln7/karmabot/munge"
	"github.com/dustin/go-humanize"
)

var (
	regexps = struct {
		Motivate, GiveKarma, QueryKarma, Leaderboard, URL, SlackUser, Throwback *regexp.Regexp
	}{
		Motivate:    karmaReg.GetMotivate(),
		GiveKarma:   karmaReg.GetGive(),
		QueryKarma:  karmaReg.GetQuery(),
		Leaderboard: regexp.MustCompile(`^karma(?:bot)? (?:leaderboard|top|highscores) ?([0-9]+)?$`),
		URL:         regexp.MustCompile(`^karma(?:bot)? (?:url|web|link)?$`),
		SlackUser:   regexp.MustCompile(`^<@([A-Za-z0-9]+)>$`),
		Throwback:   karmaReg.GetThrowback(),
	}
)

// Database is an abstraction around the database, mostly designed for use in tests.
type Database interface {
	// InsertPoints persistently records that points have been given or deducted.
	InsertPoints(points *database.Points) error

	// GetUser returns information about a user, including their current number of points.
	GetUser(name string) (*database.User, error)

	// GetLeaderboard returns the top X users with the most points, in order.
	GetLeaderboard(limit int) (database.Leaderboard, error)

	// GetTotalPoints returns the total number of points transferred across all users.
	GetTotalPoints() (int, error)

	// GetThrowback returns a random karma operation on a specific user.
	GetThrowback(user string) (*database.Throwback, error)
}

type ChatService interface {
	// IncomingEventsChan returns a channel of real-time events.
	IncomingEventsChan() chan socketmode.Event

	// Returns Socketmode client
	GetSocketClient() *socketmode.Client

    // SendMessage sends a message to a Slack channel.
    SendMessage(channel, text string, options ...slack.MsgOption) (string, string, error)

    // PostEphemeral sends an ephemeral message to a user in a channel.
    PostEphemeral(channelID, userID string, options ...slack.MsgOption) (string, error)

	// GetUserInfo retrieves the complete user information for the specified username.
	GetUserInfo(user string) (*slack.User, error)
}

// New chat code
type SlackChatService struct {
	Client  socketmode.Client
	API    	*slack.Client
}

// IncomingEventsChan returns a channel of real-time messaging events.
func (s SlackChatService) IncomingEventsChan() chan socketmode.Event {
	return s.Client.Events
}

// GetSocketClient returns the socket client
func (s SlackChatService) GetSocketClient() *socketmode.Client {
	return &s.Client
}

// SendMessage sends a message to a Slack channel.
func (s SlackChatService) SendMessage(channel, text string, options ...slack.MsgOption) (string, string, error) {
    return s.API.PostMessage(channel, append([]slack.MsgOption{slack.MsgOptionText(text, false)}, options...)...)
}


// PostEphemeral sends an ephemeral message to a user in a channel.
func (s SlackChatService) PostEphemeral(channelID, userID string, options ...slack.MsgOption) (string, error) {
    return s.API.PostEphemeral(channelID, userID, options...)
}

// GetUserInfo retrieves the complete user information for the specified username.
func (s SlackChatService) GetUserInfo(user string) (*slack.User, error) {
    return s.API.GetUserInfo(user)
}

// UserAliases is a map of alias -> main username
type UserAliases map[string]string

// ReactjiConfig contains the configuration for reactji-based votes
type ReactjiConfig struct {
	Enabled          bool
	Upvote, Downvote StringList
}

type Config struct {
	Slack                       ChatService
	Debug, Motivate, SelfKarma  bool
	MaxPoints, LeaderboardLimit int
	Log                         *log.Log
	UI                          ui.Provider
	DB                          Database
	UserBlacklist               StringList
	Aliases                     UserAliases
	Reactji                     *ReactjiConfig
	ReplyType                   string
}

type Bot struct {
	Config *Config
}

func NewBot(config *Config) *Bot {
	return &Bot{
		Config: config,
	}
}

func (b *Bot) Listen(){
    for msg := range b.Config.Slack.IncomingEventsChan() {
        fmt.Printf("Event received: %v\n", msg)
		switch msg.Type {
		case socketmode.EventTypeConnected:
			b.Config.Log.Info("Connected to Slack with Socket Mode.")
		case socketmode.EventTypeConnectionError:
			b.Config.Log.Info("Connection failed. Retrying later....")
		case socketmode.EventTypeEventsAPI:
			eventsAPIEvent, ok := msg.Data.(slackevents.EventsAPIEvent)
			b.Config.Log.KV("EventsAPIEvent", eventsAPIEvent).Info("Inner event received")
			if !ok {
				fmt.Printf("Ignored %+v\n", msg)

				continue
			}
			b.Config.Slack.GetSocketClient().Ack(*msg.Request)

			switch eventsAPIEvent.Type {
			case slackevents.CallbackEvent:
				innerEvent := eventsAPIEvent.InnerEvent
                b.Config.Log.KV("info", innerEvent).Info("Inner event received")
				//Handle slack events
				switch ev := innerEvent.Data.(type) {
				// I do not see a user of AppMentionEvent
				// case *slackevents.AppMentionEvent:
				// 	_, _, err := b.Config.Slack.GetSocketClient().PostMessage(ev.Channel, slack.MsgOptionText("Yes, hello.", false))
				// 	if err != nil {
				// 		b.Config.Log.Err(err).Error("failed posting message")
				// 	}
				case *slackevents.MessageEvent:
					fmt.Println("==========MESSAGE EVENT==========")
					go b.handleMessageEvent(ev)
				case *slackevents.ReactionAddedEvent:
					fmt.Printf("reaction %q added to message %q", ev.Reaction, ev.ItemUser)
					go b.handleReactionAddedEvent(ev)
				case *slackevents.ReactionRemovedEvent:
					fmt.Printf("reaction %q removed from message %q", ev.Reaction, ev.ItemUser)
					go b.handleReactionRemovedEvent(ev)
				}
			default:
				b.Config.Slack.GetSocketClient().Debugf("unsupported Events API event received")
			}		
		default:
            fmt.Printf("Unhandled event type: %v\n", msg.Type)
        }
    }
}

func (b *Bot) handleMessageEvent(ev *slackevents.MessageEvent) {
	if ev.Type != "message" {
		return
	}

	// convert motivates into karmabot syntax
	if b.Config.Motivate {
		if match := regexps.Motivate.FindStringSubmatch(ev.Text); len(match) > 0 {
			ev.Text = match[1] + "++ for doing good work"
		}
	}

	switch {
	case regexps.URL.MatchString(ev.Text):
		b.printURL(ev)

	case regexps.GiveKarma.MatchString(ev.Text):
		b.givePoints(ev)

	case regexps.Leaderboard.MatchString(ev.Text):
		b.printLeaderboard(ev)

	case regexps.Throwback.MatchString(ev.Text):
		b.getThrowback(ev)

	case regexps.QueryKarma.MatchString(ev.Text):
		b.queryKarma(ev)
	}
}


// SendMessage sends a message to a Slack channel.
func (b *Bot) SendMessage(message, channel, thread string) {
    _, _, err := b.Config.Slack.SendMessage(channel, message, slack.MsgOptionTS(thread))
    if err != nil {
        b.Config.Log.Err(err).Error("failed to send message")
    }
}
// SendReply sends a reply to a message, either as a new message in the channel or a thread (configurable)
func (b *Bot) SendReply(reply string, message *slackevents.MessageEvent) {
	switch b.Config.ReplyType {
	case "ephemeral":
		b.SendReplyEphemeral(reply, message)
	default:
		b.SendMessage(reply, message.Channel, b.getReplyThread(message))
	}
}

// SendReplyEphemeral sends a reply to a message as an ephemeral message to the user
func (b *Bot) SendReplyEphemeral(reply string, message *slackevents.MessageEvent) {
	b.SendMessageEphemeral(message.Channel, message.User, reply, message.ThreadTimeStamp)
}

// SendMessageEphemeral sends an ephemeral message to a user
func (b *Bot) SendMessageEphemeral(reply, channel, user, thread string) {
	b.Config.Slack.PostEphemeral(channel, user, slack.MsgOptionText(reply, false), slack.MsgOptionTS(thread))
}

func (b *Bot) getReplyThread(message *slackevents.MessageEvent) string {
	var thread string

	switch b.Config.ReplyType {
	case "message":
		thread = message.ThreadTimeStamp
	case "thread":
		if message.ThreadTimeStamp != "" {
			thread = message.ThreadTimeStamp
		} else {
			thread = message.TimeStamp
		}
	}

	return thread
}

func (b *Bot) printURL(ev *slackevents.MessageEvent) {
	url, err := b.Config.UI.GetURL("/")
	if b.handleError(err, ev) {
		return
	}

	// ui is disabled
	if url == "" {
		return
	}

	b.SendReply(url, ev)
}

func (b *Bot) handleError(err error, message *slackevents.MessageEvent) bool {
	if err == nil {
		return false
	}

	b.Config.Log.Err(err).Error("error")
	if message != nil {
		var text string
		if b.Config.Debug {
			text = err.Error()
		} else {
			text = "an error has occurred."
		}

		b.SendReply(text, message)
	}

	return true
}
func (b *Bot) givePoints(ev *slackevents.MessageEvent) {
	match := regexps.GiveKarma.FindStringSubmatch(ev.Text)
	if len(match) == 0 {
		return
	}

	// forgive me
	if match[1] != "" {
		// we matched the first alt expression
		match = match[:4]
	} else {
		// we matched the second alt expression
		match = append(match[:1], match[4:]...)
	}

	from, err := b.getUserNameByID(ev.User)
	if b.handleError(err, ev) {
		return
	}
	to, err := b.parseUser(match[1])
	if b.handleError(err, ev) {
		return
	}
	to = strings.ToLower(to)

	if _, blacklisted := b.Config.UserBlacklist[to]; blacklisted {
		b.Config.Log.KV("user", to).Info("user is blacklisted, ignoring karma command")
		return
	}

	points := min(len(match[2])-1, b.Config.MaxPoints)
	if match[2][0] == '-' {
		points *= -1
	}
	reason := match[3]

	if !b.Config.SelfKarma && from == to {
		b.SendReply("Sorry, you are not allowed to do that.", ev)
		return
	}

	record := &database.Points{
		From:   from,
		To:     to,
		Points: points,
		Reason: reason,
	}

	err = b.Config.DB.InsertPoints(record)
	if b.handleError(err, ev) {
		return
	}

	pointsMsg, err := b.getUserPointsMessage(to, reason, points)
	if b.handleError(err, ev) {
		return
	}

	b.SendReply(pointsMsg, ev)
}

func (b *Bot) getThrowback(ev *slackevents.MessageEvent) {
	match := regexps.Throwback.FindStringSubmatch(ev.Text)
	if len(match) == 0 {
		return
	}

	var (
		user string
		err  error
	)
	if match[1] != "" {
		user, err = b.parseUser(match[1])
		if b.handleError(err, ev) {
			return
		}
		user = strings.ToLower(user)
	} else {
		user, err = b.getUserNameByID(ev.User)
		if b.handleError(err, ev) {
			return
		}
	}

	throwback, err := b.Config.DB.GetThrowback(user)
	if err == database.ErrNoSuchUser {
		b.SendReply(fmt.Sprintf("could not find any karma operations for %s", user), ev)
		return
	}

	if b.handleError(err, ev) {
		return
	}

	date := humanize.Time(throwback.Timestamp)
	if throwback.Reason != "" {
		throwback.Reason = fmt.Sprintf(" for %s", throwback.Reason)
	}
	text := fmt.Sprintf("%s received %d points from %s %s%s", munge.Munge(throwback.To), throwback.Points.Points, munge.Munge(throwback.From), date, throwback.Reason)

	b.SendReply(text, ev)
}


func (b *Bot) queryKarma(ev *slackevents.MessageEvent) {
	match := regexps.QueryKarma.FindStringSubmatch(ev.Text)
	if len(match) == 0 {
		return
	}

	name, err := b.parseUser(match[1])
	if b.handleError(err, ev) {
		return
	}
	name = strings.ToLower(name)

	user, err := b.Config.DB.GetUser(name)
	switch {
	case err == database.ErrNoSuchUser:
		// override debug mode
		b.SendReply(err.Error(), ev)
	case b.handleError(err, ev):
	default:
		b.SendReply(fmt.Sprintf("%s == %d", user.Name, user.Points), ev)
	}
}

func (b *Bot) printLeaderboard(ev *slackevents.MessageEvent) {
	match := regexps.Leaderboard.FindStringSubmatch(ev.Text)
	if len(match) == 0 {
		return
	}

	limit := b.Config.LeaderboardLimit
	if match[1] != "" {
		var err error
		limit, err = strconv.Atoi(match[1])
		if b.handleError(err, ev) {
			return
		}
	}

	text := fmt.Sprintf("*top %d leaderboard*\n", limit)

	url, err := b.Config.UI.GetURL(fmt.Sprintf("/leaderboard/%d", limit))
	if b.handleError(err, ev) {
		return
	}
	if url != "" {
		text = fmt.Sprintf("%s%s\n", text, url)
	}

	leaderboard, err := b.Config.DB.GetLeaderboard(limit)
	if b.handleError(err, ev) {
		return
	}

	for i, user := range leaderboard {
		text += fmt.Sprintf("%d. %s == %d\n", i+1, munge.Munge(user.Name), user.Points)
	}

	b.SendReply(text, ev)
}

func (b *Bot) getUserNameByID(id string) (string, error) {
	userInfo, err := b.Config.Slack.GetUserInfo(id)
	if err != nil {
		return "", err
	}

	return userInfo.Name, nil
}

func (b *Bot) parseUser(user string) (string, error) {
	if match := regexps.SlackUser.FindStringSubmatch(user); len(match) > 0 {
		var err error
		user, err = b.getUserNameByID(match[1])
		if err != nil {
			return "", err
		}
	}

	// check if it is aliased
	if alias, ok := b.Config.Aliases[user]; ok {
		user = alias
	}

	return user, nil
}

func (b *Bot) getUserPointsMessage(name, reason string, points int) (string, error) {
	user, err := b.Config.DB.GetUser(name)
	if err != nil {
		return "", err
	}

	text := fmt.Sprintf("%s == %d (", name, user.Points)

	if points > 0 {
		text += "+"
	}
	text = fmt.Sprintf("%s%d", text, points)

	if reason != "" {
		text += fmt.Sprintf(" for %s", reason)
	}
	text += ")"

	return text, nil
}

func (b *Bot) handleReactionAddedEvent(ev *slackevents.ReactionAddedEvent) {
	if !b.Config.Reactji.Enabled {
		return
	}

	var (
		points int
		reason string
	)
	switch {
	case b.Config.Reactji.Upvote.Contains(ev.Reaction):
		points = +1
	case b.Config.Reactji.Downvote.Contains(ev.Reaction):
		points = -1
	default:
		return
	}

	reason = fmt.Sprintf("added a :%s: reactji", ev.Reaction)
	fmt.Printf("points %d, reason %s\n", points, reason)
	b.handleReactionEvent(ev, reason, points)
}

func (b *Bot) handleReactionRemovedEvent(ev *slackevents.ReactionRemovedEvent) {
	if !b.Config.Reactji.Enabled {
		return
	}

	var (
		points int
		reason string
	)
	switch {
	case b.Config.Reactji.Upvote.Contains(ev.Reaction):
		points = -1
	case b.Config.Reactji.Downvote.Contains(ev.Reaction):
		points = +1
	default:
		return
	}

	reason = fmt.Sprintf("removed a :%s: reactji", ev.Reaction)
	b.handleReactionEvent((*slackevents.ReactionAddedEvent)(ev), reason, points)
}

// at this point there is no difference between ReactionAddedEvent and ReactionRemovedEvent
func (b *Bot) handleReactionEvent(ev *slackevents.ReactionAddedEvent, reason string, points int) {
	// look up usernames
	from, err := b.getUserNameByID(ev.User)
	if b.handleError(err, nil) {
		return
	}
	to, err := b.getUserNameByID(ev.ItemUser)
	if b.handleError(err, nil) {
		return
	}

	// add the actor's username to the reason
	reason = fmt.Sprintf("%s %s", from, reason)

	from, to = strings.ToLower(from), strings.ToLower(to)

	// insert points
	record := &database.Points{
		From:   from,
		To:     to,
		Points: points,
		Reason: reason,
	}

	err = b.Config.DB.InsertPoints(record)
	if b.handleError(err, nil) {
		return
	}

	pointsMsg, err := b.getUserPointsMessage(to, reason, points)
	if b.handleError(err, nil) {
		return
	}

	// reply as ephemeral message
	b.SendMessageEphemeral(pointsMsg, ev.Item.Channel, ev.User, "")
}