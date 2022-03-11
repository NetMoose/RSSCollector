package main

import (
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"sort"

	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/boltdb/bolt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jessevdk/go-flags"
	"golang.org/x/net/html"
	"gopkg.in/yaml.v2"
)

type RSSItem struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

type Config struct {
	Dbpath   string `yaml:"dbpath"`
	Telegram struct {
		SendDebug bool   `yaml:"senddebug"`
		ChatId    int64  `yaml:"chatid"`
		Token     string `yaml:"token"`
	} `yaml:"telegram"`
	RssList []RSSItem `yaml:"rsslist"`
}

func NewConfig(configPath string) (*Config, error) {
	// Create config structure
	config := &Config{}

	// Open config file
	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Init new YAML decode
	d := yaml.NewDecoder(file)

	// Start YAML decoding from file
	if err := d.Decode(&config); err != nil {
		return nil, err
	}

	return config, nil
}

type Options struct {
	ConfigPath string `short:"c" long:"configpath" description:"Config file path"`
}

var ConfigPath = "./config.yml"

type Rss2 struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	// Required
	Title       string `xml:"channel>title"`
	Link        string `xml:"channel>link"`
	Description string `xml:"channel>description"`
	// Optional
	PubDate  string `xml:"channel>pubDate"`
	ItemList []Item `xml:"channel>item"`
}

type Item struct {
	// Required
	Title       string        `xml:"title"`
	Link        string        `xml:"link"`
	Description template.HTML `xml:"description"`
	// Optional
	Content  template.HTML `xml:"encoded"`
	PubDate  string        `xml:"pubDate"`
	Comments string        `xml:"comments"`
}

type SendItems struct {
	ItemList []Item
}

type ByPubDate []Item

func (a ByPubDate) Len() int { return len(a) }
func (a ByPubDate) Less(i, j int) bool {
	timeone, _ := time.Parse("Mon, 02 Jan 2006 15:04:05 -0700", a[i].PubDate)
	timetwo, _ := time.Parse("Mon, 02 Jan 2006 15:04:05 -0700", a[j].PubDate)
	return timeone.Unix() > timetwo.Unix()
}
func (a ByPubDate) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

func GetRSS(name string, url string) (*Rss2, error) {
	rss := &Rss2{}

	var netClient = &http.Client{}

	customTransport := &(*http.DefaultTransport.(*http.Transport)) // make shallow copy
	timeout := time.Duration(240 * time.Second)
	customTransport = &http.Transport{
		IdleConnTimeout:       timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     false,
		DisableCompression:    false,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		TLSHandshakeTimeout:   timeout,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   100,
		MaxConnsPerHost:       100,
	}
	netClient = &http.Client{Transport: customTransport, Timeout: timeout}
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:97.0) Gecko/20100101 Firefox/97.0")

	resp, err := netClient.Do(request)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Start RSS decoding from file
	if err := xml.Unmarshal(body, rss); err != nil {
		return nil, err
	}

	return rss, nil
}

func ProcessRss(rss Rss2, dbpath string, rssname string) (*SendItems, error) {
	var si SendItems
	db, err := bolt.Open(dbpath, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(rssname))
		if err != nil {
			return err
		}
		return nil
	})

	for _, v := range rss.ItemList {
		db.View(func(tx *bolt.Tx) error {
			// Assume bucket exists and has keys
			b := tx.Bucket([]byte(rssname))
			c := b.Cursor()
			flag := false
			for key, _ := c.First(); key != nil; key, _ = c.Next() {
				if v.Link == string(key) {
					flag = true
					break
				}
			}
			if !flag {
				si.ItemList = append(si.ItemList, v)
			}
			return nil
		})
	}

	sort.Sort(ByPubDate(si.ItemList))
	if len(si.ItemList) > 0 {
		return &si, nil
	} else {
		return nil, nil
	}
}

func in_array(val interface{}, array interface{}) (exists bool) {
	exists = false

	switch reflect.TypeOf(array).Kind() {
	case reflect.Slice:
		s := reflect.ValueOf(array)

		for i := 0; i < s.Len(); i++ {
			if reflect.DeepEqual(val, s.Index(i).Interface()) == true {
				exists = true
				return
			}
		}
	}

	return
}

func NormalizeHTMLforTelegram(s string) (out string) {

	tags := []string{"br", "img", "b", "strong", "i", "em", "code", "s", "strike", "del", "u", "pre"}

	domDocTest := html.NewTokenizer(strings.NewReader(s))
	previousStartTokenTest := domDocTest.Token()
	for {
		tt := domDocTest.Next()
		if len(out) > 2500 {
			if e := in_array(previousStartTokenTest.Data, tags) && previousStartTokenTest.Data != "img" && previousStartTokenTest.Data != "br"; e {
				out += fmt.Sprintf("</%s> ...", previousStartTokenTest.Data)
			} else {
				out += " ..."
			}
			return
		}
		switch {
		case tt == html.ErrorToken:
			return
		case tt == html.StartTagToken:
			previousStartTokenTest = domDocTest.Token()
			if e := in_array(previousStartTokenTest.Data, tags); e {
				switch {
				case previousStartTokenTest.Data == "br":
					out += "\n"
				case previousStartTokenTest.Data == "img" && previousStartTokenTest.Attr[0].Key == "src":
					out += fmt.Sprintf("%s ", previousStartTokenTest.Attr[0].Val)
				// case previousStartTokenTest.Data == "a" && previousStartTokenTest.Attr[0].Key == "href":
				// 	out += fmt.Sprintf(" %s ", previousStartTokenTest.Attr[0].Val)
				default:
					out += fmt.Sprintf(" <%s>", previousStartTokenTest.Data)
				}
			}
		case tt == html.EndTagToken:
			t := domDocTest.Token()
			if e := in_array(t.Data, tags); e {
				// switch {
				// case t.Data == "a":
				// 	out += " "
				// default:
				out += fmt.Sprintf("</%s> ", t.Data)
				// }
			}
		case tt == html.SelfClosingTagToken:
			t := domDocTest.Token()
			if e := in_array(t.Data, tags); e {
				if t.Data == "br" {
					out += "\n"
				}
			}
		case tt == html.TextToken:
			if previousStartTokenTest.Data == "script" {
				continue
			}
			TxtContent := strings.TrimSpace(html.UnescapeString(string(domDocTest.Text())))
			if len(TxtContent) > 0 {
				out += TxtContent
			}
		}
	}
}

func SendAndWriteToDB(send SendItems, dbpath string, rssname string, token string, chatid int64, debug bool) error {

	db, err := bolt.Open(dbpath, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for i := len(send.ItemList) - 1; i >= 0; i-- {
		v := send.ItemList[i]
		log.Printf("Send to telegram post: %s", v.Title)
		bot, err := tgbotapi.NewBotAPI(token)
		if err != nil {
			log.Panic(err)
		}
		bot.Debug = debug

		s := "<i>" + rssname + "</i>\n\n" + "<b>" + string(v.Title) + "</b>\n\n" + NormalizeHTMLforTelegram(html.UnescapeString(string(v.Description))) +
			"\n\n" + v.Link
		msg := tgbotapi.NewMessage(chatid, s)
		msg.ParseMode = "Html"
		_, err = bot.Send(msg)
		if err != nil {
			log.Panic(err)

		}
		duration := time.Duration(10) * time.Second
		time.Sleep(duration)

		db.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists([]byte(rssname))
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(v)
			if err != nil {
				return err
			}
			err = b.Put([]byte(v.Link), encoded)
			if err != nil {
				return err
			}
			return nil
		})
	}
	return nil
}

func main() {
	var options Options
	var parser = flags.NewParser(&options, flags.Default)
	if _, err := parser.Parse(); err != nil {
		switch flagsErr := err.(type) {
		case flags.ErrorType:
			if flagsErr == flags.ErrHelp {
				os.Exit(0)
			}
			os.Exit(1)
		default:
			os.Exit(1)
		}
	}
	log.Println("Flags processed")

	if options.ConfigPath != "" {
		ConfigPath = options.ConfigPath
	}
	// Get config
	log.Printf("Config file: %s\n", ConfigPath)
	cfg, err := NewConfig(ConfigPath)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Start to send")
	for _, v := range cfg.RssList {
		log.Printf("Feed: %s, URL: %s\n", v.Name, v.URL)
		rss, err := GetRSS(v.Name, v.URL)
		if err != nil {
			log.Fatalln(err)
		}
		send, err := ProcessRss(*rss, cfg.Dbpath, v.Name)
		if err != nil {
			log.Fatal(err)
		}
		if nil != send && len(send.ItemList) > 0 {
			if err := SendAndWriteToDB(*send, cfg.Dbpath, v.Name, cfg.Telegram.Token, cfg.Telegram.ChatId, cfg.Telegram.SendDebug); err != nil {
				log.Fatal(err)
			}
		} else {
			log.Println("Nothing to send")
		}
	}
	log.Println("Stop to send")
}
