package main

import (
	"bufio"
	"code.google.com/p/gcfg"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/nlopes/slack"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	"io"
	"io/ioutil"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ConfigFile struct {
	Profile map[string]*struct {
		Slack            string
		Admin            []string
		Default_Channel  string
		Default_Calendar string
		Calendar_Name    []string
		Calendar         []string
	}
}

type InternalMessage struct {
	*slack.MessageEvent
	Outgoing *slack.OutgoingMessage
}

func allocInternalMessage() InternalMessage {
	outgoing := new(slack.OutgoingMessage)
	outgoing.Id = int(time.Now().UnixNano())
	outgoing.ChannelId = CONFIG.Profile[TEAM].Default_Channel
	outgoing.Type = "message"

	return InternalMessage{new(slack.MessageEvent), outgoing}
}

func allocWithBoth(incoming *slack.MessageEvent, outgoing *slack.OutgoingMessage) InternalMessage {
	return InternalMessage{incoming, outgoing}
}

func allocWithOutgoing(outgoing *slack.OutgoingMessage) InternalMessage {
	return allocWithBoth(new(slack.MessageEvent), outgoing)
}

func allocWithIncoming(incoming *slack.MessageEvent) InternalMessage {
	outgoing := new(slack.OutgoingMessage)
	outgoing.Id = int(time.Now().UnixNano())
	outgoing.ChannelId = incoming.ChannelId
	outgoing.Type = incoming.Type

	return allocWithBoth(incoming, outgoing)
}

type Events []interface{}

func (e Events) Len() int {
	return len(e)
}

func (e Events) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

func (e Events) Less(i, j int) bool {
	start := e[i].(map[string]interface{})["start"].(map[string]interface{})
	end := e[j].(map[string]interface{})["end"].(map[string]interface{})

	var a, b time.Time
	a, _ = get_date_from_google_shit(start)
	b, _ = get_date_from_google_shit(end)

	return int64(a.Sub(b)) < 0
}

var CONFIG ConfigFile
var TEAM string
var KEY string
var CFGFILE string
var QTEFILE string
var TIMEZONE *time.Location
var QUOTES []string

func setupAPIClient(keyfile, authURL string) (*http.Client, error) {
	var data []byte
	var err error
	var conf *jwt.Config

	data, err = ioutil.ReadFile(keyfile)
	if err != nil {
		conf, _ = google.JWTConfigFromJSON(data, authURL)
	} else {
		conf, err = google.JWTConfigFromJSON(data, authURL)
	}

	return conf.Client(oauth2.NoContext), err
}

func call(client *http.Client, method string, args map[string]string, log chan string) ([]byte, error) {
	if method[len(method)-1] != '?' {
		method += "?"
	}
	for k, v := range args {
		if strings.Contains(method, k) {
			method = strings.Replace(method, "{"+k+"}", v, -1)
		} else {
			method += k + "=" + v + "&"
		}
	}

	log <- "CALL: Calling method: " + method
	response, err := client.Get("https://www.googleapis.com/calendar/v3" + method)

	log <- fmt.Sprintf("CALL: Got Response, len: %d", response.ContentLength)
	if err != nil {
		return nil, err
	}

	log <- "CALL: Moving Response to buffer"
	json := make([]byte, response.ContentLength)
	buffer := make([]byte, response.ContentLength)
	running_length := 0
	for int64(running_length) < response.ContentLength {
		length, err := response.Body.Read(buffer)
		for i := 0; i < length; i++ {
			json[running_length+i] = buffer[i]
		}
		running_length += length
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return json, nil
}

func receiver(chReceiver chan slack.SlackEvent, chMessage chan InternalMessage, log chan string) {
	for {
		msg, ok := <-chReceiver
		if !ok {
			return
		}
		switch msg.Data.(type) {
		case slack.HelloEvent:
			//Ignore Hello, might want a DM to me
		case *slack.MessageEvent:
			a := allocWithIncoming(msg.Data.(*slack.MessageEvent))
			chMessage <- a
		//case *slack.PresenceChangeEvent:
		//	a := msg.Data.(*slack.PresenceChangeEvent)
		case slack.LatencyReport:
			a := msg.Data.(slack.LatencyReport)

			log <- fmt.Sprintf("RECEIVER: Current Latency Report: %v", a.Value)
		case *slack.SlackWSError:
			error := msg.Data.(*slack.SlackWSError)

			log <- fmt.Sprintf("RECEIVER: Slack Error Message: %d - %s", error.Code, error.Msg)
		default:

			log <- fmt.Sprintf("RECEIVER: Unexpected / Don't Care: %+v", msg.Data)
		}
	}
}

func get_Season_From_Month(month time.Month) int {
	switch month {
	case time.January:
		fallthrough
	case time.February:
		fallthrough
	case time.March:
		return 1
	case time.April:
		fallthrough
	case time.May:
		fallthrough
	case time.June:
		return 2
	case time.July:
		fallthrough
	case time.August:
		fallthrough
	case time.September:
		return 3
	default:
		return 4
	}
}

func get_Season(season string) int {
	if season == "winter" {
		return 1
	} else if season == "spring" {
		return 2
	} else if season == "summer" {
		return 3
	} else if season == "fall" {
		return 4
	}
	return -1
}

func get_Term(term []string) (int, int) {
	var season, year int = -1, -1
	if term[0] != "" {
		season = get_Season(term[0])
	}
	if term[1] != "" {
		year, _ = strconv.Atoi(term[1])
		year = year%2000 + 2000
	}
	return season, year
}

func get_Wkday(wkday string) (time.Weekday, error) {
	err := dateParseError{input: wkday, reason: "Invalid Weekday"}
	if len(wkday) < 3 {
		return time.Sunday, err
	}
	if wkday[:3] == "mon" {
		return time.Monday, nil
	} else if wkday[:3] == "tue" {
		return time.Tuesday, nil
	} else if wkday[:3] == "wed" {
		return time.Wednesday, nil
	} else if wkday[:3] == "thu" {
		return time.Thursday, nil
	} else if wkday[:3] == "fri" {
		return time.Friday, nil
	} else if wkday[:3] == "sat" {
		return time.Saturday, nil
	} else if wkday[:3] == "sun" {
		return time.Sunday, nil
	}
	return time.Sunday, err
}

func round(val float64) int {
	if val-float64(int(val)) < 0.5 {
		return int(val)
	}
	return int(val) + 1
}

func get_term_week(date time.Time) (int, int) {
	term := get_Season_From_Month(date.Month())
	start := get_Wk1Monday(date.Year(), term)

	return term, round(float64(date.Sub(start)) / 24 / 7 / 1000000000)
}

func get_Wk1Monday(year, term int) time.Time {
	first := time.Date(year, time.Month(term*3-2), 1, 0, 0, 0, 0, TIMEZONE)
	offset := int(time.Monday - first.Weekday())
	return first.AddDate(0, 0, 14+offset)
}

type dateParseError struct {
	input  string
	reason string
}

func (e dateParseError) Error() string {
	return fmt.Sprintf("Error parsing %s: %s", e.input, e.reason)
}

func getDateFromString(date []string) time.Time {
	year, _ := strconv.Atoi(date[3])
	month, _ := strconv.Atoi(date[1])
	day, _ := strconv.Atoi(date[2])
	return time.Date(year%2000+2000, time.Month(month), day, 0, 0, 0, 0, TIMEZONE)
}

func get_date_from_term_week_wkday(year, term, week int, wkday time.Weekday) time.Time {
	t := get_Wk1Monday(year, term)
	return t.AddDate(0, 0, 7*(week-1)+int(wkday-time.Monday))
}

func getRange(rng string) (time.Time, time.Time, error) {
	date, _ := regexp.Compile("(\\d{1,2})[-/ ](\\d{1,2})[-/ ]((?:\\d\\d){1,2})")
	kulang, _ := regexp.Compile("(?i)((?:[a-zA-Z]+ ?(?:\\d\\d){1,2}?)?) ?w(?:ee)?k ?(\\d{1,2}) ?([a-z]+)?")
	season_year, _ := regexp.Compile("(?i)((Spring)|(Summer)|(Fall)|(Autumn)|(Winter)) ?(\\d*)?")
	wkday, _ := regexp.Compile("(?i)((Sun)|(Mon)|(Tues?)|(Wed(?:nes)?)|(Thur?s?)|(Fri)|(Sat)|(Sun))(?:day)?")

	now := time.Now()
	startTime := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, TIMEZONE)
	endTime := startTime.AddDate(0, 0, 1)

	if rng == "today" {
		return startTime, endTime, nil
	} else if rng == "tomorrow" {
		return endTime, endTime.AddDate(0, 0, 1), nil
	} else if strings.Trim(rng, " \t\r\n\v") == "" {
		return startTime, startTime.AddDate(0, 0, 7), nil
	} else if date.MatchString(rng) {
		startTime = getDateFromString(date.FindStringSubmatch(rng))
		endTime = startTime.AddDate(0, 0, 1)
	} else if kulang.MatchString(rng) {

		var had_wkday bool
		res := kulang.FindStringSubmatch(rng)
		year := startTime.Year()
		cTerm, cWeek := get_term_week(startTime)
		cWkday := time.Sunday

		/*Begin logic for term and year*/
		if season_year.MatchString(rng) {
			sy := season_year.FindStringSubmatch(rng)
			tmpTerm, tmpYear := get_Term(sy[1:])
			if tmpTerm == -1 {
				return startTime, endTime, dateParseError{input: rng, reason: "Invalid Term (Summer, Fall, Spring, Winter)"}
			} else {
				cTerm = tmpTerm
			}

			if tmpYear != -1 {
				year = tmpYear
			}
		}

		cWeek, _ = strconv.Atoi(res[2])
		if wkday.MatchString(res[3]) {
			had_wkday = true
			tmpWkday, err := get_Wkday(strings.ToLower(res[3]))
			if err != nil {
				return startTime, endTime, err
			}
			cWkday = tmpWkday
		}

		startTime = get_date_from_term_week_wkday(year, cTerm, cWeek, cWkday)
		if had_wkday {
			endTime = startTime.AddDate(0, 0, 1)
		} else {
			endTime = startTime.AddDate(0, 0, 7)
		}
	} else if len(strings.Trim(rng, " ")) > 0 {
		return startTime, endTime, dateParseError{input: rng, reason: "Invalid Date Format"}
	}

	return startTime, endTime, nil
}

func get_date_from_google_shit(input map[string]interface{}) (time.Time, error) {
	for k, v := range input {
		switch k {
		case "date":
			return time.Parse("2006-01-02", v.(string))
		case "dateTime":
			return time.Parse(time.RFC3339, v.(string))
		}
	}
	return time.Date(0, time.January, 1, 0, 0, 0, 0, TIMEZONE), nil
}

func format_calendar_event(response map[string]interface{}) string {

	items := response["items"].([]interface{})
	var sortable []interface{}
	for _, v := range items {
		if v != nil && v.(map[string]interface{})["status"].(string) != "cancelled" {
			sortable = append(sortable, v)
		}
	}
	sort.Sort(Events(sortable))
	items = sortable
	if len(items) == 0 {
		return ""
	}

	table := make([][4]string, len(items))

	for i, v := range items {
		d := v.(map[string]interface{})
		a, _ := get_date_from_google_shit(d["start"].(map[string]interface{}))
		e, _ := get_date_from_google_shit(d["end"].(map[string]interface{}))
		if d["summary"] == nil {
			continue
		}
		b := d["summary"].(string)
		if len(b) > 30 {
			b = b[:27] + "..."
		}
		tmp := d["location"]
		c := ""
		if tmp != nil {
			c = tmp.(string)
		} else {
			c = ""
		}
		if len(c) > 30 {
			c = c[:27] + "..."
		}

		table[i][0] = a.Format(time.Stamp)[:12]
		table[i][1] = e.Format(time.Stamp)[:12]
		table[i][2] = b
		table[i][3] = c
	}

	max_lens := [4]int{0, 0, 0, 0}
	for i, v := range max_lens {
		for _, row := range table {
			curr_len := len(row[i])
			if v < curr_len {
				max_lens[i] = curr_len
				v = curr_len
			}
		}
	}
	fmt_string := fmt.Sprintf(" %%-12s | %%-12s | %%-%ds", max_lens[2])

	reply := "``` Start        | End          | Event"
	reply += strings.Repeat(" ", Max(max_lens[2]-4, 1))
	if max_lens[3] > 0 {
		fmt_string = fmt.Sprintf(" | %%-%ds", max_lens[3])
		reply += "| Location"
	}
	reply += "\n" + strings.Repeat("-", len(reply)-3) + "\n"

	for _, row := range table {
		reply += fmt.Sprintf(fmt_string, row[0], row[1], row[2])
		if max_lens[3] > 0 {
			reply += fmt.Sprintf(fmt_string, row[3])
		}
		reply += "\n"
	}
	return reply + "```"
}

func Max(a, b int) int {
	if b > a {
		return b
	}
	return a
}

func process(chMessage chan InternalMessage, chSender chan InternalMessage, gApi *http.Client, log chan string) {
	rx, _ := regexp.Compile("^\\^(\\w+)\\s?(.+)?$")
	fully_defined, _ := regexp.Compile("(.+) ((to)|(->)) (.+)")

	for {
		msg := <-chMessage
		if msg.Text == "（╯°□°）╯︵(\\ .o.)\\" {
			msg.Outgoing.Text = "ಠ_ಠ"
			chSender <- msg
			continue
		}
		for _, v := range rx.FindAllStringSubmatch(msg.Text, -1) {
			switch strings.ToLower(v[1]) {
			case "hello":
				msg.Outgoing.Text = "Hello, world!"
				chSender <- msg
			case "hype":
				msg.Outgoing.Text = "Hype!"
				chSender <- msg
			case "events":
				args := make(map[string]string)
				var err error
				var startTime, endTime time.Time

				all_calendars := strings.Contains(v[2], "all")
				cal_id := CONFIG.Profile[TEAM].Default_Calendar

				if !all_calendars {
					for i, cal_name := range CONFIG.Profile[TEAM].Calendar_Name {
						if strings.Contains(v[2], strings.ToLower(cal_name)) {
							cal_id = CONFIG.Profile[TEAM].Calendar[i]
							v[2] = strings.Replace(v[2], cal_name, "", -1)
							break
						}
					}
				}

				if fully_defined.MatchString(v[2]) {
					res := fully_defined.FindStringSubmatch(v[2])
					startTime, _, err = getRange(res[1])
					if err != nil {
						msg.Outgoing.Text = fmt.Sprintf("'%s' isn't a date, <@%s>. Reason: %s", res[1], msg.UserId, err)
						chSender <- msg
					}

					if res[2] == "to" {
						endTime, _, err = getRange(res[3])
					} else {
						_, endTime, err = getRange(res[3])
					}

					if err != nil {
						msg.Outgoing.Text = fmt.Sprintf("'%s' isn't a date, <@%s>. Reason: %s", res[2], msg.UserId, err)
						chSender <- msg
					}
				} else {
					startTime, endTime, err = getRange(v[2])
					if err != nil {
						msg.Outgoing.Text = fmt.Sprintf("'%s' isn't a date, <@%s>. Reason: %s", v[2], msg.UserId, err)
						chSender <- msg
					}
				}

				if err == nil {
					args["calendarId"] = cal_id
					args["timeMin"] = startTime.Format(time.RFC3339)
					args["timeMax"] = endTime.Format(time.RFC3339)
					resp, err := call(gApi, "/calendars/{calendarId}/events", args, log)
					if err != nil {

						log <- "PROCESS: Error at process: " + err.Error()
					}
					var response map[string]interface{}
					if err := json.Unmarshal(resp, &response); err != nil {

						log <- "PROCESS: Error at process: " + err.Error()
						panic(err)
					}

					if len(response["items"].([]interface{})) == 0 {
						msg.Outgoing.Text = "There are no calendar events scheduled for that week."
						chSender <- msg
					} else {
						resp := format_calendar_event(response)
						if resp == "" {
							msg.Outgoing.Text = "There are no calendar events scheduled for that week."
						} else {
							msg.Outgoing.Text = resp
						}
						chSender <- msg
					}
				}
			case "restart":
				if msg.UserId == CONFIG.Profile[TEAM].Admin[0] {
					quote := quote()
					msg.Outgoing.Text = quote
					chSender <- msg
					time.Sleep(time.Second)
					panic(quote)
				}
			case "psycho": fallthrough
			case "quote":
				msg.Outgoing.Text = quote()
				chSender <- msg
			default:
				msg.Outgoing.Text = fmt.Sprintf("I don't understand what you said, <@%s>", msg.UserId)
				chSender <- msg
			}
		}
	}
}

func update_every_morning(gApi *http.Client, chSender chan InternalMessage, log chan string) {
	args := make(map[string]string)
	args["calendarId"] = CONFIG.Profile[TEAM].Default_Calendar
	var next_morning time.Time

	for {
		t := time.Now().Local()
		if t.Hour() < 7 {
			next_morning = time.Date(t.Year(), t.Month(), t.Day(), 7, 0, 0, 0, TIMEZONE)
		} else {
			next_morning = time.Date(t.Year(), t.Month(), t.Day()+1, 7, 0, 0, 0, TIMEZONE)
		}
		day := time.Date(next_morning.Year(), next_morning.Month(), next_morning.Day(), 0, 0, 0, 0, TIMEZONE)
		args["timeMin"] = day.Format(time.RFC3339)
		args["timeMax"] = day.AddDate(0, 0, 1).Format(time.RFC3339)

		post := "Good Morning!\n"
		msg := allocInternalMessage()

		log <- fmt.Sprintf("MORNING_UPDATE: Making Request:\t%+v", args)
		resp, err := call(gApi, "/calendars/{calendarId}/events", args, log)
		if err != nil {

			log <- "PMORNING_UPDATE: Error making Calendar Request: " + err.Error()
			panic(err)
		}

		log <- "MORNING_UPDATE: Successfully Requested Calendar Events"

		var response map[string]interface{}

		log <- "MORNING_UPDATE: Converting Request to JSON"
		err = json.Unmarshal(resp, &response)
		if err != nil {

			log <- "MORNING_UPDATE: Error converting response to JSON: " + err.Error()
			panic(err)
		}

		log <- "MORNING_UPDATE: Successfully converted response to JSON"

		if len(response["items"].([]interface{})) == 0 {
			msg.Outgoing.Text = post + "There are no events happening today."
		} else {
			msg.Outgoing.Text = post + "Here are the events happening today:\n" + format_calendar_event(response)
		}

		time.Sleep(next_morning.Sub(t))

		log <- "MORNING_UPDATE: Posting Morning Message"
		chSender <- msg
	}
}

func recurring_notifier(gApi *http.Client, chSender chan InternalMessage, log chan string) {
	args := make(map[string]string)
	args["calendarId"] = CONFIG.Profile[TEAM].Default_Calendar
	var next_morning time.Time
	var midnight time.Time

	for {
		t := time.Now().In(TIMEZONE)
		midnight = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, TIMEZONE)
		next_morning = midnight.AddDate(0, 0, 1)

		args["timeMin"] = midnight.Format(time.RFC3339)
		args["timeMax"] = next_morning.Format(time.RFC3339)

		log <- fmt.Sprintf("NOTIFIER: Making Request:\t%+v", args)
		resp, err := call(gApi, "/calendars/{calendarId}/events", args, log)
		if err != nil {

			log <- "NOTIFIER: Error making Calendar Request: " + err.Error()
			continue
		}

		var response map[string]interface{}

		log <- "NOTIFIER: Converting response to JSON"
		if err := json.Unmarshal(resp, &response); err != nil {

			log <- "NOTIFIER: Error converting response to JSON: " + err.Error()
			continue
		}

		log <- "NOTIFIER: Successfully converted response to JSON"

		for _, entry := range response["items"].([]interface{}) {
			event := entry.(map[string]interface{})
			if event["summary"] == nil {
				continue
			}
			for k, v := range event["start"].(map[string]interface{}) {
				switch k {
				case "dateTime":

					log <- "NOTIFIER: Found non-All-Day event, parsing time."
					start, err := time.Parse(time.RFC3339, v.(string))
					if err != nil {

						log <- "NOTIFIER: Error parsing date from google: " + v.(string)
						continue
					}

					log <- "NOTIFIER: Successfully parsed time. Setting up notifiers"
					if start.Sub(t) > 0 {
						go wait_to_notify(event, start, time.Hour, chSender)
						go wait_to_notify(event, start, time.Minute*10, chSender)
					}
				}
			}
		}
		time.Sleep(next_morning.Sub(time.Now().In(TIMEZONE)))
	}
}

func wait_to_notify(event map[string]interface{}, start time.Time, before time.Duration, chSender chan InternalMessage) {
	msg := allocInternalMessage()
	msg.Outgoing.Text = fmt.Sprintf("Hey Guys! Dont forget, %s is coming up in %v!:\n", event["summary"].(string), before)

	time.Sleep(start.Add(before * -1).Sub(time.Now().In(TIMEZONE)))
	chSender <- msg
}

func log(fname *os.File, incoming chan string) {
	var log string

	for {
		writer := bufio.NewWriter(fname)
		log = <-incoming
		line := "[" + time.Now().Format(time.RFC3339) + "]:\t" + log

		cmd := exec.Command("echo",  line)
		stdoutPipe, err := cmd.StdoutPipe()
    if err != nil {
        fmt.Println(err)
				continue
    }

    err = cmd.Start()
    if err != nil {
        fmt.Println(err)
				continue
    }

    go io.Copy(writer, stdoutPipe)
    cmd.Wait()
		writer.Flush()
	}
}

func prep_quotes() {
	stats, err := os.Stat(QTEFILE)
	if err != nil {
		fmt.Println("Failed to load quotes:")
		fmt.Println(err)
		panic(err)
	}
	quotefile, err := os.OpenFile(QTEFILE, os.O_RDONLY, 0666)
	if err != nil {
		fmt.Println("Failed to load quotes:")
		fmt.Println(err)
		panic(err)
	}

	qtes := make([]byte, stats.Size())
	buffer := make([]byte, stats.Size())
	running_length := 0
	for int64(running_length) < stats.Size() {
		length, err := quotefile.Read(buffer)
		for i := 0; i < length; i++ {
			qtes[running_length+i] = buffer[i]
		}
		running_length += length
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Println("Failed to load quotes:")
			fmt.Println(err)
			panic(err)
		}
	}

	QUOTES = strings.Split(string(qtes[:stats.Size()]), "\n")
	quotefile.Close()
}

func quote() string {
	val, err := rand.Int(rand.Reader, big.NewInt(int64(len(QUOTES))))
	if err != nil {
		fmt.Println("Failed to get random value:\n", err)
		panic(err)
	}
	return QUOTES[val.Int64()]
}

func init() {
	flag.StringVar(&KEY, "key", "key.json", "Calendar Api Key")
	flag.StringVar(&KEY, "k", "key.json", "Calendar Api Key (shorthand)")
	flag.StringVar(&CFGFILE, "config", "config.gcfg", "Config File Name")
	flag.StringVar(&CFGFILE, "c", "config.gcfg", "Config File Name (shorthand)")
	flag.StringVar(&QTEFILE, "quote", "quote.txt", "Quote File Name")
	flag.StringVar(&QTEFILE, "q", "quote.txt", "Quote File Name (shorthand)")
}

func main() {
	flag.Parse()
	for i, arg := range os.Args[1:] {
		if os.Args[i][0] != '-' || strings.Contains(os.Args[i], "=") || arg[0] != '-' {
			TEAM = arg
			break
		}
	}

	prep_quotes()

	chSender := make(chan InternalMessage, 10)
	chReceiver := make(chan slack.SlackEvent, 10)
	chMessage := make(chan InternalMessage, 10)
	fname := time.Now().Format(time.RFC3339)

	logFile, err := os.Create("log/" + fname + ".log")
	if err != nil {
		fmt.Println("STARTUP: Error at creating START logfile:\t" + err.Error())
		panic(err)
	}
	chStart := make(chan string, 10)
	go log(logFile, chStart)

	err = gcfg.ReadFileInto(&CONFIG, CFGFILE)
	if err != nil {
		chStart <- "STARTUP: Error at loading config file:\t" + err.Error()
		panic(err)
	}
	chStart <- "STARTUP: Successfully loaded the Config File:\t" + CFGFILE

	TIMEZONE, err = time.LoadLocation("America/Detroit")
	if err != nil {
		chStart <- "STARTUP: Error at loading Timezone:\t" + err.Error()
		panic(err)
	}

	gApi, err := setupAPIClient(KEY, "https://www.googleapis.com/auth/calendar")
	if err != nil {
		chStart <- "STARTUP: Error when loading the Calendar API:\t" + err.Error()
		panic(err)
	}
	chStart <- "STARTUP: Successfully loaded the Calendar API"

	api := slack.New(CONFIG.Profile[TEAM].Slack)
	api.SetDebug(false)
	wsAPI, err := api.StartRTM("", "http://localhost/")
	if err != nil {
		chStart <- "STARTUP: Error when starting websocket:\t" + err.Error()
		panic(err)
	}
	chStart <- "STARTUP: Successfully opened the websocket"

	go wsAPI.HandleIncomingEvents(chReceiver)
	go wsAPI.Keepalive(20 * time.Second)
	go process(chMessage, chSender, gApi, chStart)
	go func(wsAPI *slack.SlackWS, outbox chan InternalMessage, log chan string) {
		for {
			select {
			case msg := <-outbox:

				log <- fmt.Sprintf("OUTBOX: Sending Message: %s\n", msg.Outgoing.Text)
				wsAPI.SendMessage(msg.Outgoing)
			}
		}
	}(wsAPI, chSender, chStart)

	go update_every_morning(gApi, chSender, chStart)
	go recurring_notifier(gApi, chSender, chStart)
	chStart <- "STARTUP: Successfully loaded all main threads. Starting Receiver"

	receiver(chReceiver, chMessage, chStart)
}
