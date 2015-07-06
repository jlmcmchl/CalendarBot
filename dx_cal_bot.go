package main

import (
	"code.google.com/p/gcfg"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/nlopes/slack"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var CALENDAR_ID string
var SLACK_TOKEN string
var LOGFILE string
var LOGLEN int64 = 0

func setupAPIClient(keyfile, authURL string) (*http.Client, error) {
	data, err := ioutil.ReadFile(keyfile)
	conf, err := google.JWTConfigFromJSON(data, authURL)

	return conf.Client(oauth2.NoContext), err
}

func call(client *http.Client, method string, args map[string]string) ([]byte, error) {
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
	log("Calling method: " + method)
	response, err := client.Get("https://www.googleapis.com/calendar/v3" + method)
	log("Got Response, len: " + strconv.Itoa(int(response.ContentLength)))
	if err != nil {
		return nil, err
	}
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

func receiver(chReceiver chan slack.SlackEvent, chMessage chan slack.MessageEvent) {
	for {
		select {
		case msg, ok := <-chReceiver:
			if !ok {
				return
			}
			fmt.Print("Event Received: ")
			switch msg.Data.(type) {
			case slack.HelloEvent:
				//Ignore Hello, might want a DM to me
			case *slack.MessageEvent:
				a := msg.Data.(*slack.MessageEvent)
				fmt.Printf("Message: %v", a)
				chMessage <- *a
			case *slack.PresenceChangeEvent:
				a := msg.Data.(*slack.PresenceChangeEvent)
				fmt.Printf("Presence Change: %v", a)
			case slack.LatencyReport:
				a := msg.Data.(slack.LatencyReport)
				fmt.Printf("Current latency: %v", a.Value)
			case *slack.SlackWSError:
				error := msg.Data.(*slack.SlackWSError)
				fmt.Printf("Error: %d - %s", error.Code, error.Msg)
			default:
				fmt.Printf("Unexpected: %v", msg.Data)
			}
			fmt.Println()
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
	first := time.Date(year, time.Month(term*3-2), 1, 0, 0, 0, 0, time.Local)
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
	return time.Date(year%2000+2000, time.Month(month), day, 0, 0, 0, 0, time.Local)
}

func get_date_from_term_week_wkday(year, term, week int, wkday time.Weekday) time.Time {
	t := get_Wk1Monday(year, term)
	return t.AddDate(0, 0, 7*(week-1)+int(wkday-time.Monday))
}

func getRange(rng string) (time.Time, time.Time, error) {
	date, _ := regexp.Compile("(\\d{1,2})[-/ ](\\d{1,2})[-/ ]((?:\\d\\d){1,2})")
	kulang, _ := regexp.Compile("(?i)((?:[a-zA-Z]+ ?(?:\\d\\d){1,2}?)?) ?w(?:ee)?k ?(\\d{1,2}) ?([a-z]+)?")
	season_year, _ := regexp.Compile("(?i)(\\w+) ?(\\d*)")
	wkday, _ := regexp.Compile("(?i)((Sun)|(Mon)|(Tues?)|(Wed(?:nes)?)|(Thur?s?)|(Fri)|(Sat)|(Sun))(?:day)?")

	now := time.Now()
	startTime := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	endTime := startTime.AddDate(0, 0, 1)

	if date.MatchString(rng) {
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

type Events []interface{}

func (e Events) Len() int {
	return len(e)
}

func (e Events) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

func (e Events) Less(i, j int) bool {
	start := e[i].(map[string]interface{})["start"].(map[string]interface{})
	end := e[j].(map[string]interface{})["start"].(map[string]interface{})

	var a, b time.Time
	a, _ = get_date_from_google_shit(start)
	b, _ = get_date_from_google_shit(end)

	return int64(a.Sub(b)) < 0
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
	return time.Date(0, time.January, 1, 0, 0, 0, 0, time.Local), nil
}

func format_calendar_event(response map[string]interface{}) string {

	items := response["items"].([]interface{})
	sort.Sort(Events(items))

	table := make([][4]string, len(items))

	for i, v := range items {
		d := v.(map[string]interface{})
		a, _ := get_date_from_google_shit(d["start"].(map[string]interface{}))
		e, _ := get_date_from_google_shit(d["end"].(map[string]interface{}))
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

	reply := "``` Start        | End          | Event"
	reply += strings.Repeat(" ", max_lens[2]-5)
	if max_lens[3] > 0 {
		reply += " | Location"
	}
	reply += "\n" + strings.Repeat("-", len(reply)-3) + "\n"

	for _, row := range table {
		fmt_string := fmt.Sprintf(" %%-12s | %%-12s | %%-%ds", max_lens[2])
		reply += fmt.Sprintf(fmt_string, row[0], row[1], row[2])
		if max_lens[3] > 0 {
			fmt_string = fmt.Sprintf(" | %%-%ds", max_lens[3])
			reply += fmt.Sprintf(fmt_string, row[3])
		}
		reply += "\n"
	}
	return reply + "```"
}

func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func process(chMessage chan slack.MessageEvent, chSender chan slack.OutgoingMessage, gApi *http.Client) {
	id := 1
	rx, _ := regexp.Compile("(?:\\s|(?:--)|^)\\^(\\w+)\\s?(.+)?(?:(?:--)|$)")
	fully_defined, _ := regexp.Compile("(.+) to (.+)")

	for {
		msg := <-chMessage
		fmt.Println("%s", msg.Type)
		if msg.Text == "（╯°□°）╯︵(\\ .o.)\\" {
			chSender <- slack.OutgoingMessage{Id: id, ChannelId: msg.ChannelId, Text: "ಠ_ಠ", Type: msg.Type}
			id++
		}
		for _, v := range rx.FindAllStringSubmatch(msg.Text, -1) {
			switch strings.ToLower(v[1]) {
			case "hello":
				chSender <- slack.OutgoingMessage{Id: id, ChannelId: msg.ChannelId, Text: "Hello, world!", Type: msg.Type}
				id++
			case "events":
				args := make(map[string]string)
				var err error
				var startTime, endTime time.Time
				if fully_defined.MatchString(v[2]) {
					res := fully_defined.FindStringSubmatch(v[2])
					startTime, _, err = getRange(res[1])
					if err != nil {
						chSender <- slack.OutgoingMessage{Id: id, ChannelId: msg.ChannelId, Text: fmt.Sprintf("'%s' isn't a date, <@%s>", res[1], msg.UserId), Type: msg.Type}
						id++
					}

					endTime, _, err = getRange(res[2])
					if err != nil {
						chSender <- slack.OutgoingMessage{Id: id, ChannelId: msg.ChannelId, Text: fmt.Sprintf("'%s' isn't a date, <@%s>", res[2], msg.UserId), Type: msg.Type}
						id++
					}
				} else {
					startTime, endTime, err = getRange(v[2])
					if err != nil {
						chSender <- slack.OutgoingMessage{Id: id, ChannelId: msg.ChannelId, Text: fmt.Sprintf("'%s' isn't a date, <@%s>", v[2], msg.UserId), Type: msg.Type}
						id++
					}
				}

				if err == nil {
					args["calendarId"] = CALENDAR_ID
					args["timeMin"] = startTime.Format(time.RFC3339)
					args["timeMax"] = endTime.Format(time.RFC3339)
					resp, err := call(gApi, "/calendars/{calendarId}/events", args)
					if err != nil {
						log("Error at process: " + err.Error())
					}
					var response map[string]interface{}
					if err := json.Unmarshal(resp, &response); err != nil {
						log("Error at process: " + err.Error())
						panic(err)
					}

					if len(response["items"].([]interface{})) == 0 {
						chSender <- slack.OutgoingMessage{Id: id, ChannelId: msg.ChannelId, Text: "There are no calendar events scheduled for that week.", Type: msg.Type}
					} else {
						chSender <- slack.OutgoingMessage{Id: id, ChannelId: msg.ChannelId, Text: format_calendar_event(response), Type: msg.Type}
					}
					id++
				}
			default:
				chSender <- slack.OutgoingMessage{Id: id, ChannelId: msg.ChannelId, Text: fmt.Sprintf("I don't understand what you said, <@%s>", msg.UserId), Type: msg.Type}
				id++
			}
		}
	}
}

func update_every_morning(gApi *http.Client, chSender chan slack.OutgoingMessage) {
	args := make(map[string]string)
	args["calendarId"] = CALENDAR_ID
	id := 0
	var next_morning time.Time

	for {
		t := time.Now()
		if t.Hour() < 7 {
			next_morning = time.Date(t.Year(), t.Month(), t.Day(), 7, 0, 0, 0, time.Local)
		} else {
			next_morning = time.Date(t.Year(), t.Month(), t.Day()+1, 7, 0, 0, 0, time.Local)
		}
		day := time.Date(next_morning.Year(), next_morning.Month(), next_morning.Day(), 0, 0, 0, 0, time.Local)
		args["timeMin"] = day.Format(time.RFC3339)
		args["timeMax"] = day.AddDate(0, 0, 1).Format(time.RFC3339)

		post := "Good Morning!\n"

		time.Sleep(next_morning.Sub(t))

		resp, err := call(gApi, "/calendars/{calendarId}/events", args)
		if err != nil {
			log("Error at update_every_morning: " + err.Error())
			panic(err)
		}

		var response map[string]interface{}
		if err := json.Unmarshal(resp, &response); err != nil {
			log("Error at update_every_morning: " + err.Error())
			panic(err)
		}

		if len(response["items"].([]interface{})) == 0 {
			chSender <- slack.OutgoingMessage{Id: 0, ChannelId: "C04PMF9PX", Text: post + "There are no events happening today.", Type: "message"}
		} else {
			chSender <- slack.OutgoingMessage{Id: 0, ChannelId: "C04PMF9PX", Text: post + "Here are the events happening today:\n" + format_calendar_event(response), Type: "message"}
		}

		id++
	}
}

func log(log string) {
	bytes := []byte(time.Now().Format(time.RFC3339) + " " + log + "\n")
	logFile, err := os.OpenFile(LOGFILE, os.O_RDWR, 0666)
	if err != nil {
		fmt.Println(err)
		fmt.Println(log)
		panic(err)
	}
	written, err := logFile.WriteAt(bytes, LOGLEN)
	if err != nil {
		fmt.Println(err)
		fmt.Println(log)
		panic(err)
	}
	LOGLEN += int64(written)
}

type Config struct {
	Profile map[string]*struct {
		Slack    string
		Calendar string
	}
}

var team, key, config string

func init() {
	flag.StringVar(&key, "key", "key.json", "Calendar Api Key")
	flag.StringVar(&key, "k", "key.json", "Calendar Api Key (shorthand)")
	flag.StringVar(&config, "config", "config.gcfg", "Config File Name")
	flag.StringVar(&config, "c", "config.gcfg", "Config File Name (shorthand)")
}

func main() {
	flag.Parse()
	for i, arg := range os.Args[1:] {
		if os.Args[i][0] != '-' || strings.Contains(os.Args[i], "=") || arg[0] != '-' {
			team = arg
			break
		}
	}

	chSender := make(chan slack.OutgoingMessage, 10)
	chReceiver := make(chan slack.SlackEvent, 10)
	chMessage := make(chan slack.MessageEvent, 10)
	LOGFILE = "log/" + time.Now().Format(time.RFC3339) + ".log"
	_, err := os.Create(LOGFILE)
	if err != nil {
		panic(err)
	}

	var cfg Config
	err = gcfg.ReadFileInto(&cfg, config)
	if err != nil {
		log(err.Error())
		panic(err)
	}
	CALENDAR_ID = cfg.Profile[team].Calendar
	SLACK_TOKEN = cfg.Profile[team].Slack

	gApi, err := setupAPIClient(key, "https://www.googleapis.com/auth/calendar")
	if err != nil {
		log("Error @ main: " + err.Error())
		panic(err)
	} else {
		log("Successfully loaded the Calendar API")
	}

	api := slack.New(SLACK_TOKEN)
	api.SetDebug(false)
	wsAPI, _ := api.StartRTM("", "http://localhost/")

	go wsAPI.HandleIncomingEvents(chReceiver)
	go wsAPI.Keepalive(20 * time.Second)
	go process(chMessage, chSender, gApi)
	go func(wsAPI *slack.SlackWS, chSender chan slack.OutgoingMessage) {
		for {
			select {
			case msg := <-chSender:
				wsAPI.SendMessage(&msg)
			}
		}
	}(wsAPI, chSender)

	go update_every_morning(gApi, chSender)

	receiver(chReceiver, chMessage)

}
