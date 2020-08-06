package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/garyburd/go-oauth/oauth"
	"github.com/joeshaw/envdecode"
)

// First we create a connection to Twitter's streaming APIs
// The dial function ensures that conn is first closed and then opens a new conenction
// and keeps the conn variable updated with the current connection.
// IF the connection dies, we redial without worrying about zombie connections.
var (
	conn          net.Conn
	reader        io.ReadCloser
	authClient    *oauth.Client
	creds         *oauth.Credentials
	authSetUpOnce sync.Once
	httpClient    *http.Client
)

func dial(ctx context.Context, netw, addr string) (net.Conn, error) {
	if conn != nil {
		conn.Close()
		conn = nil
	}
	netc, err := net.DialTimeout(netw, addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	conn = netc
	return netc, nil
}

// tweet structure
type tweet struct {
	Text string
}

// Connection is periodically closed and a new one initiated to reload options from the database
//  at regular intervals. The closeConn function handles this by closing the connection
// and also closes io.ReadCloser, which is used to read the body of responses

func closeConn() {
	if conn != nil {
		conn.Close()
	}
	if reader != nil {
		reader.Close()
	}
}

func setupTwitterAuth() {
	// A struct type to store the environment variables required to authenticate with twitter.
	var ts struct {
		ConsumerKey    string `env:"TWITTER_KEY,required"`
		ConsumerSecret string `env:"TWITTER_SECRET,required"`
		AccessToken    string `env:"TWITTER_ACCESS_TOKEN,required"`
		AccessSecret   string `env:"TWITTER_ACCESS_SECRET,required"`
	}
	if err := envdecode.Decode(&ts); err != nil {
		log.Fatalln(err)
	}

	creds = &oauth.Credentials{
		Token:  ts.AccessToken,
		Secret: ts.AccessSecret,
	}
	authClient = &oauth.Client{
		Credentials: oauth.Credentials{
			Token:  ts.ConsumerKey,
			Secret: ts.ConsumerSecret,
		},
	}

}

// Takes a send only channel called votes; this is how this function
// will inform the rest of our program that it has noticed a vote on Twitter
func readFromTwitter(votes chan<- string) {
	// load options from all the polls data
	options, err := loadOptions()
	if err != nil {
		log.Println("Failed to load options:", err)
		return
	}

	// create a url.URL object that describes the appropriate endpoint
	u, err := url.Parse("https://stream.twitter.com/1.1/statuses/filter.json")
	if err != nil {
		log.Println("Creating filter request failed:", err)
		return
	}

	// build a url.Values object called query, set options as a comma-separated list
	query := make(url.Values)
	query.Set("track", strings.Join(options, ","))

	// Make a POST request using the encoded url.Values object (query) as the body
	req, err := http.NewRequest("POST", u.String(), strings.NewReader(query.Encode()))
	if err != nil {
		log.Println("creating filter request failed:", err)
		return
	}

	// Pass it to makeRequest along with the query object itself
	resp, err := makeRequest(req, query)
	if err != nil {
		log.Println("making request failed:", err)
		return
	}

	// make a new json.Decoder from the body of the request
	reader := resp.Body
	decoder := json.NewDecoder(reader)

	// keep reading inside an infinite for loop by calling the Decode method
	for {
		// Decode tweet into t
		var t tweet
		if err := decoder.Decode(&t); err != nil {
			break
		}

		// Iterate over all possible options, if the tweet has mentioned it, 
		// we send it on the votes channel.
		for _, option := range options {
			if strings.Contains(
				strings.ToLower(t.Text),
				strings.ToLower(option),
			) {
				log.Println("vote:", option)
				votes <- option
			}
		}
	}
}

func makeRequest(req *http.Request, params url.Values) (*http.Response, error) {
	// sync.Once is used to ensure initialization code gets run only once
	authSetUpOnce.Do(func() {
		setupTwitterAuth()
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: dial,
				// Dial: dial,
			},
		}
	})
	formEnc := params.Encode()
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Content-Length", strconv.Itoa(len(formEnc)))
	req.Header.Set("Authorization", authClient.AuthorizationHeader(creds, "POST", req.URL, params))
	return httpClient.Do(req)
}

// startTwitterStream takes in a recieve only channel (stopchan) to recieve signals on when the goroutine should stop.
// A send only channel (votes)
func startTwitterStream(stopchan <-chan struct{}, votes chan<- string) <-chan struct{} {
	stoppedchan := make(chan struct{}, 1)
	go func() {
		defer func() {
			stoppedchan <- struct{}{}
		}()
		for {
			select {
			case <-stopchan:
				log.Println("Stopping Twitter...")
				return
			default:
				log.Println("Querying Twitter...")
				readFromTwitter(votes)
				log.Println(" (waiting)")
				time.Sleep(10 * time.Second) // wait before reconnecting
			}
		}
	}()
	return stoppedchan
}
